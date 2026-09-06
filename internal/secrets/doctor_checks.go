// Purpose: the five named secrets health checks `cascade doctor` runs:
//
//	keychain-reachable, keys-resolvable, oauth-not-expired,
//	patterns-loaded and quarantine-depth. This file owns the checks that
//	do not vary by platform, plus the shared construction; check (a)'s
//	per-platform note lives in the doctor_checks_{darwin,linux,windows}.go
//	siblings.
//
// Inputs: a DoctorCheckDeps the composition root fills: the vault broker,
//
//	the quarantine ledger, the detector, the vault key names config
//	references and the quarantine depth threshold.
//
// Outputs: []doctor.Check. Every check reports names, counts and reasons.
//
//	No check emits a stored value on any path, including its error paths.
//
// Constraints: fail closed (Art.1) - a probe that could not run its
//
//	subject reports StatusError, never StatusOK. Reads go through the
//	package-internal non-elevated path, so a health run never raises an
//	interactive attestation prompt and never fails outright in a release
//	binary that refuses elevated verbs.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (internal/secrets doctor_checks.go).

package secrets

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultQuarantineThreshold is the pending-entry count above which the
// quarantine-depth check reports a warning.
const DefaultQuarantineThreshold = 25

// DoctorCheckDeps carries what the five checks probe. Every field is
// injected: nothing here reaches for the environment, the real keychain
// or the real clock.
type DoctorCheckDeps struct {
	// Broker is the vault. Required.
	Broker *Broker
	// Quarantine is the detector's ledger. Required.
	Quarantine *QuarantineStore
	// Detector supplies the loaded pattern library. Required.
	Detector *Detector
	// ConfigKeys are the vault entry names configuration references. An
	// empty list means configuration references none, which the
	// keys-resolvable check reports as such rather than as a pass.
	ConfigKeys []string
	// QuarantineThreshold is the warning depth; zero uses the default.
	QuarantineThreshold int
	// Now supplies the current time for the oauth expiry comparison.
	// Required: there is no fallback to the system clock here.
	Now func() time.Time
}

// NewDoctorChecks builds the five checks. It refuses rather than dropping
// a check it cannot build: a doctor report silently missing its vault
// checks looks identical to one whose vault is healthy.
func NewDoctorChecks(deps DoctorCheckDeps) ([]doctor.Check, error) {
	switch {
	case deps.Broker == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: doctor checks need a vault broker")
	case deps.Quarantine == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: doctor checks need a quarantine ledger")
	case deps.Detector == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: doctor checks need a detector")
	case deps.Now == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: doctor checks need a clock")
	}
	if deps.QuarantineThreshold <= 0 {
		deps.QuarantineThreshold = DefaultQuarantineThreshold
	}
	return []doctor.Check{
		&keychainReachableCheck{deps: deps},
		&keysResolvableCheck{deps: deps},
		&oauthNotExpiredCheck{deps: deps},
		&patternsLoadedCheck{deps: deps},
		&quarantineDepthCheck{deps: deps},
	}, nil
}

// keychainReachableCheck probes the custody backend the broker selected.
type keychainReachableCheck struct{ deps DoctorCheckDeps }

func (keychainReachableCheck) Name() string { return "secrets/keychain-reachable" }

func (keychainReachableCheck) Describe() string {
	return "probes the custody backend the vault selected, and reports which backend answered"
}

func (keychainReachableCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: true, Fixable: false}
}

func (keychainReachableCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run lists the vault's names. Listing is the narrowest real probe
// available: it reaches the backend and returns no value.
//
// On a tier-2 platform the documented refusal is informational and the
// check passes: the encrypted file vault is the supported store there,
// and reporting its absence of a keychain as a failure would tell every
// operator on that platform that a correctly configured host is broken.
func (c keychainReachableCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if note := keychainPlatformNote(); note != "" {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: note}, nil
	}
	names, err := c.deps.Broker.List(ctx)
	if err != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "the vault custody backend did not answer",
			Detail:      err.Error(),
			Remediation: "check that the platform secret store is unlocked and reachable for this user",
		}, nil
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: fmt.Sprintf("custody backend %q answered with %d entr%s", c.deps.Broker.Backend(), len(names), plural(len(names))),
	}, nil
}

// keysResolvableCheck asserts every vault key configuration references is
// readable.
type keysResolvableCheck struct{ deps DoctorCheckDeps }

func (keysResolvableCheck) Name() string { return "secrets/keys-resolvable" }

func (keysResolvableCheck) Describe() string {
	return "reads every vault key the configuration references, through the non-elevated path so no attestation prompt is raised"
}

func (keysResolvableCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: true, Fixable: false}
}

func (keysResolvableCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run resolves each configured key. It uses the package-internal
// non-elevated read on purpose: a health probe must not raise an
// interactive attestation prompt, and in a release binary the elevated
// verb is refused outright, which would make this check fail everywhere
// it matters most.
func (c keysResolvableCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if len(c.deps.ConfigKeys) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "configuration references no vault keys"}, nil
	}
	var unresolved []string
	for _, name := range c.deps.ConfigKeys {
		value, err := internalGet(ctx, c.deps.Broker, name)
		if err != nil || len(value) == 0 {
			unresolved = append(unresolved, name)
			continue
		}
		zeroAll([][]byte{value})
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     fmt.Sprintf("%d of %d configured vault key(s) did not resolve", len(unresolved), len(c.deps.ConfigKeys)),
			Detail:      strings.Join(unresolved, ", "),
			Remediation: "store the missing entries with `cascade vault set NAME`, or correct the names in config.toml",
		}, nil
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: fmt.Sprintf("all %d configured vault key(s) resolve", len(c.deps.ConfigKeys)),
	}, nil
}

// VaultRefsIn returns the distinct vault reference NAMES the typed tags
// in text point at, sorted. It is what the composition root feeds
// DoctorCheckDeps.ConfigKeys: a configuration value spelled as a tag is
// exactly a vault key the installation depends on resolving.
//
// A malformed tag-like run contributes nothing, matching scanTags' own
// fail-closed reading: something that does not parse as a tag is not a
// vault reference, and guessing a name out of it would have the doctor
// report a missing key nobody ever configured.
func VaultRefsIn(text string) []string {
	seen := map[string]bool{}
	for _, span := range scanTags(text) {
		tag, err := ParseTag([]byte(text[span.start:span.end]))
		if err != nil {
			continue
		}
		seen[tag.Name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// plural returns the "y"/"ies" tail for an entry count.
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
