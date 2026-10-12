package doctor

// Purpose: report the registry verification key source through the existing
//   doctor CheckRegistry and Runner.
// Inputs: a RegistryPubkeyProvider (internal/runtime.Config's [registry]
//   fields, read through a small adapter at the composition root).
// Outputs: CheckResult naming embedded or pubkey_path; unreadable overrides warn.
// Constraints: Art.1 — an unreadable config is status=error, never a
//   silent ok. An override is ok only when the client would accept it:
//   base64 of exactly one 32-byte Ed25519 public key.
// SPORT: doctor/registry-pubkey (ADD, P1-E24-W5-S50-T2).

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strings"
)

// RegistryPubkeyProvider supplies the effective [registry] URL and pubkey
// path. *runtime.Config satisfies this via a small adapter at the
// composition root (internal/runtime is not imported by this package,
// matching Art.10.2's layering — see retrieval_fusion.go's own
// FusionEnabledProvider for the identical reasoning).
type RegistryPubkeyProvider interface {
	// RegistryURL is the effective [registry].url, or "" when unset.
	RegistryURL() string
	// RegistryPubkeyPath is the effective [registry].pubkey_path, or ""
	// when unset.
	RegistryPubkeyPath() string
}

// registryPubkeyCheck implements Check for the registry_pubkey probe.
type registryPubkeyCheck struct {
	config RegistryPubkeyProvider
}

// NewRegistryPubkeyCheck builds the registry_pubkey Check.
func NewRegistryPubkeyCheck(config RegistryPubkeyProvider) Check {
	return &registryPubkeyCheck{config: config}
}

func (c *registryPubkeyCheck) Name() string { return "registry_pubkey" }

func (c *registryPubkeyCheck) Describe() string {
	return "reports whether the plugin registry (R-14.75) has a usable [registry].pubkey_path " +
		"configured alongside its [registry].url"
}

func (c *registryPubkeyCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: false, Fixable: false}
}

func (c *registryPubkeyCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{}, ErrCheckNotFixable
}

// Run reports the resolved source and refuses to call an unreadable override OK.
func (c *registryPubkeyCheck) Run(_ context.Context) (CheckResult, error) {
	if c.config == nil {
		return CheckResult{Status: StatusError, Message: "no config provided to the registry pubkey check"}, nil
	}
	if c.config.RegistryURL() == "" {
		return CheckResult{Status: StatusOK, Message: "no [registry].url configured; plugin search uses the builtin catalog only"}, nil
	}
	if c.config.RegistryPubkeyPath() != "" {
		raw, err := os.ReadFile(c.config.RegistryPubkeyPath())
		if err != nil {
			return CheckResult{Status: StatusWarn, Message: "registry key source: pubkey_path (unreadable)", Remediation: "set [registry].pubkey_path to a readable Ed25519 public key file"}, nil
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) != ed25519.PublicKeySize {
			return CheckResult{Status: StatusWarn, Message: "registry key source: pubkey_path (not a base64 32-byte Ed25519 public key)", Remediation: "set [registry].pubkey_path to a file holding the base64 Ed25519 public key"}, nil
		}
		return CheckResult{Status: StatusOK, Message: "registry key source: pubkey_path"}, nil
	}
	return CheckResult{Status: StatusOK, Message: "registry key source: embedded"}, nil
}
