// Purpose (this file): ConfigFromRuntime, the ONE way a production
// internal/ci.Config is ever built from internal/runtime's parsed
// [ci].affected_cmd (final-confirm round 3 fix). The round-1/round-2
// builds left ci.Config.AffectedCmd unsourced in production (nothing
// ever copied runtime.Config.AffectedCmd into it); this ticket's own AC
// ("internal/ci/affected_cmd.go reads it from there, not from an
// unsourced field") requires this mapping to exist here, even though the
// actual daemon call site (internal/daemon/subsystems_ci.go's
// NewCISubsystem, per P1-E32-W6-S65-T2's own contract text) is built by
// a later ticket -- see internal/build/testonly-allow.json's entry for
// this symbol.
//
// Inputs: a loaded *runtime.Config (internal/runtime/config.go's Load).
// Outputs: a Config whose AffectedCmd is runtime.Config.AffectedCmd,
// verbatim -- no re-parsing, no tokenizing, no default substitution.
// Constraints: this is the ONLY production constructor for Config; a
// bare Config{...} struct literal is test-only (see the test-only-usage
// gate's own allow-list precedent for the same class of gap as
// ChangedPaths/SelectTargets).
// SPORT: internal.ci.ConfigFromRuntime/ADDED (P1-E32-W6-S65-T1, final-confirm round 3).

package ci

import "github.com/acamarata/cascade/internal/runtime"

// ConfigFromRuntime builds a Config from c, the loaded runtime CI config
// (internal/runtime.Config -- internal/ci already imports internal/runtime
// elsewhere in this package, e.g. runner.go/attention.go/run_cmd.go, so
// this is not a new dependency). AffectedCmd is mapped verbatim, matching
// config_ci.go's own parseCIAffectedCmdField contract: empty means "not
// configured", a non-empty value is never re-parsed or tokenized here.
func ConfigFromRuntime(c runtime.Config) Config {
	return Config{AffectedCmd: c.AffectedCmd}
}
