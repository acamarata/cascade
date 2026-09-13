// Purpose: the R-21.32 closed eight-value scheduler Mode enum, its
//   fail-closed parser, and ModeForLifecycleStage -- the TOTAL R-21.49
//   mapping from the AH/S-69.T1 13-stage dev-shop lifecycle to a Mode.
//
// Inputs: ParseMode/ModeForLifecycleStage take plain strings. This
//   package deliberately does not import internal/jobs (Art.10.2 -- the
//   AO/S-79.T4 dispatch path is a caller of this package, so an economics
//   import of jobs would close a cycle), so ModeForLifecycleStage takes
//   the STRING value of internal/jobs.LifecycleStage rather than that
//   type itself. There is no second LifecycleStage type, stage list, or
//   parser here -- the 13 literal stage strings below are copied from
//   internal/jobs/lifecycle_stage.go's own constants for comparison only.
//
// Outputs: Mode values, or ErrUnknownMode/ErrUnknownLifecycleStage for
//   anything outside the closed vocabularies -- no permissive zero value
//   (06-FORGE-SPEC.md §5.15).
//
// Constraints: Modes()/ModeForLifecycleStage read only package-level data,
//   never a recomputed or derived list, so a future R-21.32/R-21.49
//   amendment is a one-line edit here.
//
// SPORT: fleet/economics/mode/ADD (P1-E41-W9-S79-T2).

package economics

import "github.com/acamarata/cascade/pkg/cascade"

// Mode is the R-21.32 closed, eight-member scheduler-mode vocabulary. The
// zero value is invalid.
type Mode string

// The eight closed Mode members, in R-21.32's written order.
const (
	ModeDiscover  Mode = "discover"
	ModePlan      Mode = "plan"
	ModeBuild     Mode = "build"
	ModeCrunch    Mode = "crunch"
	ModeIntegrate Mode = "integrate"
	ModeVerify    Mode = "verify"
	ModeRelease   Mode = "release"
	ModeIncident  Mode = "incident"
)

// modes is the single source Modes/ParseMode both read -- data, never
// recomputed, in the exact R-21.32 written order.
var modes = []Mode{
	ModeDiscover, ModePlan, ModeBuild, ModeCrunch,
	ModeIntegrate, ModeVerify, ModeRelease, ModeIncident,
}

// Modes returns the eight R-21.32 Mode values in written order.
func Modes() []Mode {
	return append([]Mode{}, modes...)
}

// Valid reports whether m is one of the eight closed members.
func (m Mode) Valid() bool {
	for _, v := range modes {
		if v == m {
			return true
		}
	}
	return false
}

// ErrUnknownMode is returned by ParseMode/ModeMultiplier/UtilityWeights
// for any string outside the eight R-21.32 modes. Fail-closed: there is
// no permissive zero value.
var ErrUnknownMode = cascade.New(cascade.KindInvalidInput, "economics: unknown scheduler mode")

// ErrUnknownLifecycleStage is returned by ModeForLifecycleStage for any
// input outside the AH/S-69.T1 13-stage lifecycle enum. This package
// declares its own sentinel (rather than reusing internal/jobs's
// identically-named one) because it cannot import internal/jobs -- see
// this file's package doc comment.
var ErrUnknownLifecycleStage = cascade.New(cascade.KindInvalidInput, "economics: unknown lifecycle stage")

// ParseMode parses s into one of the eight R-21.32 modes. Fail-closed:
// an unrecognised string returns ErrUnknownMode rather than a zero-value
// Mode.
func ParseMode(s string) (Mode, error) {
	m := Mode(s)
	if !m.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", s)
	}
	return m, nil
}

// lifecycleStageMode is the R-21.32 + R-21.49 TOTAL mapping from each of
// the 13 AH/S-69.T1 lifecycle stage strings to its Mode. Copied verbatim
// as the string literals internal/jobs/lifecycle_stage.go declares as its
// own LifecycleStage constants -- this package never imports that type
// (see package doc comment), so the strings are the only shared contract.
// No stage leaves the mode unchanged (R-21.49): every one of the 13
// stages maps to exactly one of discover/plan/build/verify/integrate/
// release, and crunch/incident are reachable only by an explicit Set,
// never derived from a lifecycle stage.
var lifecycleStageMode = map[string]Mode{
	"intent":          ModeDiscover,
	"scope":           ModeDiscover,
	"plan":            ModePlan,
	"decompose_lease": ModePlan,
	"implement":       ModeBuild,
	"cr":              ModeVerify,
	"qa":              ModeVerify,
	"adversarial":     ModeVerify,
	"integrate":       ModeIntegrate,
	"clean_node_ci":   ModeIntegrate,
	"release_cd_gate": ModeRelease,
	"accept":          ModeVerify,
	"learn":           ModeBuild,
}

// ModeForLifecycleStage resolves stage (the string value of an
// internal/jobs.LifecycleStage) to its R-21.32 + R-21.49 mode. TOTAL over
// the 13-stage enum: every stage maps to a mode, and an unrecognised
// string returns ErrUnknownLifecycleStage rather than a default mode
// (fail-closed, 06-FORGE-SPEC.md §5.15).
func ModeForLifecycleStage(stage string) (Mode, error) {
	m, ok := lifecycleStageMode[stage]
	if !ok {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownLifecycleStage, "economics: unknown lifecycle stage %q", stage)
	}
	return m, nil
}
