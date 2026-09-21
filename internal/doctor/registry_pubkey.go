package doctor

// Purpose: the registry_pubkey doctor finding (P1-E24-W5-S50-T2, D1):
//   reports when [registry].url names a plugin registry but
//   [registry].pubkey_path is unset or unreadable — the fail-closed state
//   R-14.75 requires (never a fabricated in-binary default public key; see
//   internal/runtime/config_registry.go's header for why no production
//   key exists in this tree yet). It follows retrieval_fusion.go's own
//   precedent exactly: a Check constructed over an injected interface,
//   registered and exercised through the real CheckRegistry + Runner in
//   this package's own test (registry_pubkey_test.go), and separately
//   mounted at cmd/cascade's composition root (doctor_mounts.go) — a
//   files_scope deviation this ticket records the same way
//   retrieval_fusion.go's own header records its own.
// Inputs: a RegistryPubkeyProvider (internal/runtime.Config's [registry]
//   fields, read through a small adapter at the composition root).
// Outputs: CheckResult — status=warn naming the fail-closed gap when a
//   registry URL is configured but no usable pubkey is; status=ok
//   otherwise (no registry configured, or a pubkey is present).
// Constraints: Art.1 — an unreadable config is status=error, never a
//   silent ok; this check never itself decides whether the key at
//   PubkeyPath is valid (that is Ed25519Verifier's job at call time), only
//   whether one is configured at all.
// SPORT: doctor/registry-pubkey (ADD, P1-E24-W5-S50-T2).

import "context"

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

// Run reports the fail-closed gap: a configured registry URL with no
// pubkey_path. Unconfigured (no URL at all) is StatusOK — nothing to
// verify a key for. A URL WITH a pubkey_path is also StatusOK here: this
// check names the CONFIGURATION gap only, not whether the key at that
// path actually verifies the registry's signature (that is a runtime
// fact, checked on every fetch, not a static doctor probe).
func (c *registryPubkeyCheck) Run(_ context.Context) (CheckResult, error) {
	if c.config == nil {
		return CheckResult{Status: StatusError, Message: "no config provided to the registry pubkey check"}, nil
	}
	if c.config.RegistryURL() == "" {
		return CheckResult{Status: StatusOK, Message: "no [registry].url configured; plugin search uses the builtin catalog only"}, nil
	}
	if c.config.RegistryPubkeyPath() != "" {
		return CheckResult{Status: StatusOK, Message: "[registry].pubkey_path is configured"}, nil
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: "[registry].url is set but [registry].pubkey_path is not",
		Detail: "R-14.75 requires a dedicated registry signing key; this tree ships no in-binary default " +
			"(see internal/runtime/config_registry.go), so an absent pubkey_path fails plugin search closed " +
			"to the builtin catalog only",
		Remediation: "set [registry].pubkey_path to the registry's Ed25519 public key file",
	}, nil
}
