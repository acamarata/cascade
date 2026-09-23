// Purpose: `cascade doctor`'s ci_tier2 check (P1-E25-W5-S97-T1, R-14.297
//
//	items 2/3/6) — closes the AC S-51.T3 left unmet: neither
//	`cascade github ci wait` nor `cascade github ci watch add`'s
//	Windows tier-2 refusal (internal/ci.PlatformRefusal,
//	ci.PlatformRefusal-backed watch add) was ever reported by
//	`cascade doctor` at all.
//
// Inputs: the goos this check is constructed with (never probed at Run
//
//	time — the platform is a build/runtime fact, not something a check
//	needs to detect itself).
//
// Outputs: doctor.Check registered under "ci_tier2".
// Constraints: StatusOK on every platform, unconditionally — tier-2
//
//	refusal on Windows is the SPECIFIED behaviour (internal/ci's own
//	waitDaemonAbsentRefusal / PlatformRefusal), not a defect, so this
//	check must never report StatusWarn or StatusError for it: a warn
//	row would make `cascade doctor` exit non-zero on every fresh
//	Windows install and fail the S-58.T6 clean-machine gate.
//
// SPORT: cmd/cascade doctor_ci_tier2.go (ADD) — P1-E25-W5-S97-T1.
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
)

// ciTier2WindowsDetail is the exact detail text the ticket contract
// requires on Windows, naming both affected verbs.
const ciTier2WindowsDetail = "cascade github ci wait and cascade github ci watch add need the daemon; " +
	"Windows runs tier-2 (no daemon) and these verbs refuse there"

// ciTier2UnixDetail is the analogous informational text on a platform
// where the daemon is available and neither verb refuses for this reason.
const ciTier2UnixDetail = "cascade github ci wait and cascade github ci watch add run through the daemon " +
	"on this platform; the Windows tier-2 (no-daemon) refusal does not apply here"

// ciTier2Check implements doctor.Check for the ci_tier2 probe. goos is the
// platform seam: doctor_mounts.go registers ciTier2Check{goos: runtime.GOOS}
// and tests construct it directly with "windows"/"linux" rather than
// depending on the GOOS the test binary happens to run under.
type ciTier2Check struct {
	goos string
}

func (c ciTier2Check) Name() string { return "ci_tier2" }

func (c ciTier2Check) Describe() string {
	return "reports the Windows tier-2 refusal of `cascade github ci wait` and `cascade github ci watch add`"
}

func (c ciTier2Check) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Run always reports StatusOK (see file-level Constraints): tier-2 refusal
// on Windows is specified behaviour, not something for doctor to flag.
func (c ciTier2Check) Run(ctx context.Context) (doctor.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "context already done before the check could run", Detail: err.Error()}, nil
	}
	if c.goos == "windows" {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: ciTier2WindowsDetail}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: ciTier2UnixDetail}, nil
}

// Fix is not implemented: tier-2 refusal is not a fixable condition, it is
// the documented behaviour of this platform.
func (c ciTier2Check) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
