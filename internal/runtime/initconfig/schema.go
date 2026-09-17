// Package initconfig owns `cascade init`'s non-interactive input: the
// cascade.init/v1 TOML schema, the CASCADE_INIT_* environment map, and
// the precedence that merges them into one spec.
//
// It has NO dependency on internal/runtime/init. The wizard consumes a
// spec this package produced; this package knows nothing about steps.
package initconfig

// Purpose: the cascade.init/v1 TOML schema and its strict parser
//   (P1-E16-W4-S35-T7, 08-INIT-CONFIG-SPEC §2).
// Inputs: bytes from a file a person wrote, which is untrusted input.
// Outputs: an *InitConfig, or a refusal naming what is wrong with it.
// Constraints: the parser is STRICT — an unknown key is a refusal with a
//   nearest-match suggestion, not a silently ignored line. A typo'd key
//   in a setup file produces a machine configured differently from what
//   its author wrote, and nothing tells them.
// SPORT: internal/runtime/initconfig schema (ADD) — P1-E16-W4-S35-T7.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/pkg/cascade"
	toml "github.com/pelletier/go-toml/v2"
)

// SchemaVersion is the only schema this build reads.
const SchemaVersion = "cascade.init/v1"

// InitConfig is a parsed cascade-init.toml.
type InitConfig struct {
	// Schema must equal SchemaVersion exactly.
	Schema string `toml:"schema"`
	// Profile preselects the wizard's step 2.
	Profile string `toml:"profile"`
	// Plugins drives step 4.
	Plugins PluginsConfig `toml:"plugins"`
	// Providers drives step 5. Each entry is a DIRECTIVE consumed once
	// at init, never a record the runtime keeps.
	Providers []ProviderDirective `toml:"providers"`
	// Harnesses drives step 6.
	Harnesses HarnessConfig `toml:"harnesses"`
	// Server drives step 3 on the server profile.
	Server ServerConfig `toml:"server"`
	// Telemetry drives step 7.
	Telemetry TelemetryConfig `toml:"telemetry"`
	// Daemon drives step 8.
	Daemon DaemonConfig `toml:"daemon"`
}

// PluginsConfig selects catalog entries.
type PluginsConfig struct {
	// Enable names plugins to turn on.
	Enable []string `toml:"enable"`
	// Disable names plugins to leave off.
	Disable []string `toml:"disable"`
	// Harness is auto, all or none.
	Harness string `toml:"harness"`
}

// ProviderDirective is one provider to add.
//
// Every field is a directive consumed once. KeyEnv holds the NAME of an
// environment variable, never a key: a setup file is a file people
// commit, and the single most likely way this surface leaks a credential
// is by accepting one written into it.
type ProviderDirective struct {
	// Name is the provider's name in the registry.
	Name string `toml:"name"`
	// Kind pins the provider shape, when the author would rather not
	// let the probe decide.
	Kind string `toml:"kind"`
	// Auth is "key" or "key-env". "oauth" is refused; see
	// ErrOAuthNonInteractive.
	Auth string `toml:"auth"`
	// KeyEnv names the environment variable holding the key.
	KeyEnv string `toml:"key_env"`
	// BaseURL overrides the provider's endpoint.
	BaseURL string `toml:"base_url"`
	// Verify runs the live micro-verify before recording the provider.
	Verify bool `toml:"verify"`
}

// HarnessConfig drives detection and installation.
type HarnessConfig struct {
	// Detect runs harness detection. False skips step 6 entirely.
	Detect bool `toml:"detect"`
	// Install names the harnesses to wire, from the detected set.
	Install []string `toml:"install"`
}

// ServerConfig carries the server profile's storage references. Every
// field is an env-var NAME; a literal is refused.
type ServerConfig struct {
	PostgresDSNEnv string `toml:"postgres_dsn_env"`
	RedisURLEnv    string `toml:"redis_url_env"`
	S3EnvPrefix    string `toml:"s3_env_prefix"`
}

// TelemetryConfig is step 7's answer.
//
// Enabled defaults to false and is only ever turned on by an explicit
// true here. CASCADE_TELEMETRY=0 always wins over it: telemetry is
// opt-in, and a setup file cannot opt somebody in over their environment.
type TelemetryConfig struct {
	Enabled bool `toml:"enabled"`
}

// DaemonConfig is step 8's answer.
type DaemonConfig struct {
	Install bool `toml:"install"`
}

// The auth modes a directive may name.
const (
	// AuthKey reads the key from the terminal, which a non-interactive
	// run cannot do — so on this path it means "from KeyEnv".
	AuthKey = "key"
	// AuthKeyEnv takes the key from the named environment variable.
	AuthKeyEnv = "key-env"
	// AuthOAuth is refused on this path; see ErrOAuthNonInteractive.
	AuthOAuth = "oauth"
)

// ErrOAuthNonInteractive refuses a browser flow in a file-driven run.
//
// R-14.53. A non-interactive run has nobody at the browser, so an OAuth
// directive can only hang or fail confusingly. The refusal happens BEFORE
// any step executes, so a run that cannot finish does not first change
// the machine.
var ErrOAuthNonInteractive = cascade.New(cascade.KindInvalidInput,
	`cascade init: a [[providers]] entry names auth="oauth", which needs a browser and a person `+
		`at it. A file-driven run has neither. Use auth="key-env" with key_env naming the variable `+
		"that holds the key, or run `cascade init` interactively to complete the browser flow.")

// Parse decodes raw into an InitConfig, strictly.
//
// Strict on purpose: go-toml's DisallowUnknownFields turns a typo into a
// refusal rather than a silently ignored line. A setup file whose
// misspelled key did nothing produces a machine configured differently
// from what its author wrote, and nothing anywhere says so.
func Parse(raw []byte) (*InitConfig, error) {
	var cfg InitConfig
	dec := toml.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, decodeError(err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeError turns go-toml's error into a taxonomy refusal, adding a
// nearest-match suggestion when the failure was an unknown key.
func decodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		key := strictKey(strict)
		if suggestion := nearestKey(key); suggestion != "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"cascade init: unknown key %q in the setup file; did you mean %q?", key, suggestion)
		}
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: unknown key %q in the setup file", key)
	}
	return cascade.Wrap(cascade.KindInvalidInput, err, "cascade init: the setup file is not valid TOML")
}

// strictKey names the first unknown key the strict decoder rejected.
//
// The FIRST, not all of them: a suggestion is only useful one key at a
// time, and an operator fixing a typo re-runs and sees the next. The
// dotted form is kept ("plugins.enabel" rather than "enabel") because a
// bare leaf name is ambiguous across tables.
func strictKey(strict *toml.StrictMissingError) string {
	if strict == nil || len(strict.Errors) == 0 {
		return ""
	}
	return strings.Join(strict.Errors[0].Key(), ".")
}

// Validate checks everything the type system cannot.
func (c *InitConfig) Validate() error {
	if c.Schema == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: the setup file has no schema field; it must be %q", SchemaVersion)
	}
	if c.Schema != SchemaVersion {
		// A future version is NAMED rather than ignored: reading a file
		// written for a newer schema with this parser means acting on
		// whichever fields it happens to recognise, which is a machine
		// configured from half a document.
		return cascade.Newf(cascade.KindUnsupported,
			"cascade init: the setup file declares schema %q; this build reads %q — upgrade cascade, "+
				"or write the file against this version", c.Schema, SchemaVersion)
	}
	for i := range c.Providers {
		if err := c.Providers[i].validate(i); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one provider directive.
func (p ProviderDirective) validate(index int) error {
	if p.Name == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: [[providers]] entry %d has no name", index+1)
	}
	if p.Auth == AuthOAuth {
		return ErrOAuthNonInteractive
	}
	if p.Auth != "" && p.Auth != AuthKey && p.Auth != AuthKeyEnv {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: provider %q names auth %q; this build accepts %q or %q here",
			p.Name, p.Auth, AuthKey, AuthKeyEnv)
	}
	if p.KeyEnv == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: provider %q has no key_env; a file-driven run reads the key from the "+
				"environment variable key_env names, never from the file itself", p.Name)
	}
	// A kind is checked HERE, at the file, with a nearest match — the same
	// treatment an unknown KEY already gets. An author who wrote
	// "openai_compat" should learn it from their own input, not from a
	// probe failing against a real endpoint several steps later.
	if p.Kind != "" && !knownDriverKind(p.Kind) {
		if suggestion := nearestDriverKind(p.Kind); suggestion != "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"cascade init: provider %q names kind %q; did you mean %q?", p.Name, p.Kind, suggestion)
		}
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: provider %q names kind %q; this build knows %s",
			p.Name, p.Kind, strings.Join(driverKinds, ", "))
	}
	return nil
}

// driverKinds are the kinds a [[providers]] entry may pin, read from the
// intake pipeline that has to honour them.
//
// Read rather than re-spelled. A second copy of this list drifts, and the
// way it drifts is the quiet one: a kind in the file's list and not the
// pipeline's parses here and fails at the probe, several steps later and
// against a real endpoint.
var driverKinds = func() []string {
	kinds := intake.DriverKinds()
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}()

// knownDriverKind reports whether kind is one this build knows.
func knownDriverKind(kind string) bool {
	for _, known := range driverKinds {
		if kind == known {
			return true
		}
	}
	return false
}

// nearestDriverKind suggests the closest known kind, or "" when nothing is
// close enough. Same threshold as the unknown-key suggestion, and the same
// reason for the silence: one bad guess teaches an author to ignore every
// later one.
func nearestDriverKind(kind string) string {
	best, bestDistance := "", editDistance(kind, "")+1
	for _, known := range driverKinds {
		if d := editDistance(kind, known); d < bestDistance {
			best, bestDistance = known, d
		}
	}
	if bestDistance > len(kind)/2+1 {
		return ""
	}
	return best
}

// String renders a directive for a diagnostic, naming the variable and
// never a value.
func (p ProviderDirective) String() string {
	return fmt.Sprintf("%s (key from %s)", p.Name, p.KeyEnv)
}
