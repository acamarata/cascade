// Package intake implements the universal provider intake command
// (`cascade provider add`, P1-E10-W3-S20-T1): credential resolution
// through the vault/OAuth broker seam, the normative anthropic-compat ->
// openai-compat -> gemini shape probe, model enumeration, the R-14.88
// capability probe, live micro-verify, and idempotent registry upsert
// (including --pool key-pool join). Auth paths: --key (stdin), --key-env
// <VAR> (a non-interactive env-var name, never a literal), and --oauth
// (the PKCE loopback flow; CASCADE_NO_INPUT=1 refuses it before any
// listener opens). See ParseInitConfig for the cascade-init.toml
// [[providers]] non-interactive equivalent (08-INIT-CONFIG-SPEC.md §2).
//
// Purpose (this file, config.go): the two non-HTTP input surfaces - the
//
//	interactive-flag request (AddRequest) and the non-interactive
//	cascade-init.toml [[providers]] directive - plus the CASCADE_NO_INPUT
//	and secret-literal guards both surfaces share.
//
// Inputs: cobra-parsed flag values or a parsed cascade-init.toml document.
// Outputs: a validated AddRequest, or a typed refusal.
// Constraints: key_env NAMES an environment variable; it is never a
//
//	literal value, and a secret-shaped literal in that position is a hard
//	error (Art.1) rather than a silently-accepted credential. Fail closed
//	on anything unparseable (AGENT-BRIEF).
//
// SPORT: provider.intake/ADD (P1-E10-W3-S20-T1).
package intake

import (
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// noInputEnvVar is the environment variable that forbids every interactive
// step (a browser launch, a stdin/TTY key prompt).
const noInputEnvVar = "CASCADE_NO_INPUT"

// CredentialMode names which of the three mutually-exclusive credential
// sources an AddRequest uses.
type CredentialMode uint8

const (
	// CredentialUnset is the invalid zero value.
	CredentialUnset CredentialMode = iota
	// CredentialKey reads a literal value from stdin/TTY at intake time.
	CredentialKey
	// CredentialKeyEnv reads the named environment variable once.
	CredentialKeyEnv
	// CredentialOAuth runs the PKCE loopback flow.
	CredentialOAuth
)

// AddRequest is the fully-parsed, validated input to Add - the shared
// shape both the interactive `provider add` flags and a non-interactive
// [[providers]] directive resolve to.
type AddRequest struct {
	// Name is the provider name; the registry's primary key.
	Name string
	// Credential selects the auth path.
	Credential CredentialMode
	// KeyValue is the literal key value for CredentialKey. Held only in
	// memory for the duration of Add; never logged, never returned.
	KeyValue []byte
	// KeyEnvVar names the environment variable for CredentialKeyEnv.
	KeyEnvVar string
	// BaseURL overrides the probed/default API root.
	BaseURL string
	// NoVerify skips the live micro-verify (and the capability probe,
	// which is itself a live call) and records a warning instead.
	NoVerify bool
	// Pool joins this provider to the named key-pool. Empty = standalone.
	Pool string
}

// Validate fails closed on a request this build cannot safely act on.
func (r AddRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errEmptyProviderName()
	}
	switch r.Credential {
	case CredentialKey, CredentialKeyEnv, CredentialOAuth:
	case CredentialUnset:
		return errNoCredentialSource()
	default:
		return errNoCredentialSource()
	}
	return nil
}

// noInput reports whether CASCADE_NO_INPUT=1 forbids an interactive step.
func noInput(getenv func(string) string) bool {
	return getenv != nil && getenv(noInputEnvVar) == "1"
}

// looksLikeSecret reports whether value would be flagged as credential
// material by the H/S-15.T3 detector at its default, precision-first
// threshold - the same check that gates the quarantine writer, reused
// here so a key_env directive cannot smuggle a literal past the ONE
// detector this repo trusts to make that call.
func looksLikeSecret(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		// A detector this repo cannot construct is not "assume safe": it
		// is "assume the worst", because the whole point of this check is
		// to fail closed, and a nil-detector bypass would be silent.
		return true
	}
	return len(detector.ScanCertain([]byte(value))) > 0
}

// initProviderDirective is one [[providers]] table (08-INIT-CONFIG-SPEC.md
// §2). Field names match the TOML keys exactly.
type initProviderDirective struct {
	Name    string `toml:"name"`
	Kind    string `toml:"kind"`
	Auth    string `toml:"auth"`
	KeyEnv  string `toml:"key_env"`
	BaseURL string `toml:"base_url"`
	Verify  *bool  `toml:"verify"`
	Pool    string `toml:"pool"`
}

// initDocument is the subset of cascade-init.toml this package parses.
type initDocument struct {
	Providers []initProviderDirective `toml:"providers"`
}

// ParseInitConfig parses raw as a cascade-init.toml document and returns
// its [[providers]] directives translated to AddRequest values. Any
// directive this build cannot understand - an unknown kind, a key_env
// value that is not an env-var name, a literal secret in key_env's
// position - is a hard, whole-parse refusal (fail closed on the batch,
// never a partially-admitted subset).
func ParseInitConfig(raw []byte) ([]AddRequest, error) {
	var doc initDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "intake: cascade-init.toml did not parse as TOML")
	}
	out := make([]AddRequest, 0, len(doc.Providers))
	for _, d := range doc.Providers {
		req, err := directiveToRequest(d)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// directiveToRequest validates and translates one [[providers]] table.
func directiveToRequest(d initProviderDirective) (AddRequest, error) {
	if strings.TrimSpace(d.Name) == "" {
		return AddRequest{}, errUnparseableInitConfig("a [[providers]] entry is missing name")
	}
	if !DriverKind(d.Kind).Valid() {
		return AddRequest{}, errUnparseableInitConfig("provider \"" + d.Name + "\" names an unrecognised kind \"" + d.Kind + "\"")
	}
	req := AddRequest{Name: d.Name, BaseURL: d.BaseURL, Pool: d.Pool}
	if d.Verify != nil {
		req.NoVerify = !*d.Verify
	}
	if err := directiveCredential(&req, d); err != nil {
		return AddRequest{}, err
	}
	return req, nil
}

// directiveCredential resolves one directive's credential fields onto req.
func directiveCredential(req *AddRequest, d initProviderDirective) error {
	switch d.Auth {
	case "oauth":
		req.Credential = CredentialOAuth
		return nil
	case "key", "":
		if strings.TrimSpace(d.KeyEnv) == "" {
			return errUnparseableInitConfig("provider \"" + d.Name + "\" needs key_env for key auth")
		}
		if looksLikeSecret(d.KeyEnv) {
			return errSecretLiteralInKeyEnv(d.Name)
		}
		req.Credential = CredentialKeyEnv
		req.KeyEnvVar = d.KeyEnv
		return nil
	default:
		return errUnparseableInitConfig("provider \"" + d.Name + "\" names an unrecognised auth mode \"" + d.Auth + "\"")
	}
}
