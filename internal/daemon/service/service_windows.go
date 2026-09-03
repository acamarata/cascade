//go:build windows

package service

// Purpose: the Windows tier-2 refusal (R-14.131: a platform that only
//   COMPILES here must never claim parity it cannot demonstrate). Windows
//   has no daemon at all (06-FORGE-SPEC §2), so `cascade daemon install`/
//   `uninstall` refuse unconditionally with the SAME typed error kind and
//   message shape internal/daemon/lifecycle_windows.go already established
//   for every other daemon verb on this platform — see CONTRACT-
//   DEVIATIONS in this ticket's journal for why this is a pkg/cascade
//   taxonomy error rather than the contract's literal
//   ErrUnsupportedPlatform{Verb,Platform,Hint} struct: R-14.2/R-14.3 froze
//   the error taxonomy at exactly 14 Kinds precisely so no package invents
//   its own error type, and lifecycle_windows.go (this SAME wave, D/S-06.
//   T2) already proved the pattern this file reuses verbatim.
// Inputs: none read; Config is accepted for interface parity but never
//   inspected.
// Outputs: always a non-nil KindUnsupported error; the zero-value
//   DeltaReport.
// Constraints: this file must never import anything filesystem/exec-
//   related — the refusal is unconditional, matching lifecycle_windows.go.
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).

import "github.com/acamarata/cascade/pkg/cascade"

// windowsServiceRefusalHint mirrors internal/daemon/lifecycle_windows.go's
// windowsRefusalHint wording so both refusals read as one consistent
// platform story to a user running `cascade daemon install` on Windows.
const windowsServiceRefusalHint = "cascade has no daemon service management on Windows (tier-2); " +
	"commands run daemonless on Windows via the automatic socket-probe fallback (D/S-07.T4); " +
	"daemon service management is not supported on Windows in v2.0.0"

// NewInstaller returns the Windows refusal Installer.
func NewInstaller() Installer { return windowsInstaller{} }

type windowsInstaller struct{}

// Install always refuses on Windows.
func (windowsInstaller) Install(Config) (DeltaReport, error) {
	return DeltaReport{}, cascade.New(cascade.KindUnsupported, "daemon install: "+windowsServiceRefusalHint)
}

// Uninstall always refuses on Windows.
func (windowsInstaller) Uninstall(Config) (DeltaReport, error) {
	return DeltaReport{}, cascade.New(cascade.KindUnsupported, "daemon uninstall: "+windowsServiceRefusalHint)
}
