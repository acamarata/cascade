// Package governor (calibrate.go) implements the governor's calibrate
// function: scoring a *Sampler snapshot against presets.go's table and
// recommending the closest-matching HardwarePreset (P1-E13-W3-S26-T4).
//
// CONTRACT DEVIATION (documented per AGENT-BRIEF's "quote both sides"
// rule): the ticket's full_desc says calibrate "reads sampler-reported
// CPU count, available memory, and disk class". T1's actual
// ResourceSnapshot (sampler.go, outside this ticket's files_scope) has
// no CPU-count field (only CPUFraction, a load-derived utilization
// ratio — not a core count) and no disk-class field of any kind. This
// ticket may not add either: sampler.go belongs to S-26.T1, and "no
// platform-specific code in this ticket beyond calling S-26.T1 sampler
// APIs" forbids inventing a new platform probe here to fill the gap.
// calibrate therefore scores on MemTotalBytes alone — the one static
// hardware-capacity signal ResourceSnapshot actually exposes, and also
// the exact axis 17-INTAKE row 22's two cited fitness targets (16 GB,
// 128 GB) are stated on. CPU count and disk class are not scored; a
// future amendment to T1's ResourceSnapshot would be the honest way to
// add them, not a fabricated number here (Art.2).
//
// A second, deliberate substitution: "available memory" (a momentary,
// constantly-changing quantity) is read here as MemTotalBytes (a static
// hardware envelope) rather than MemTotalBytes-MemUsedBytes. A hardware
// ENVELOPE preset must reflect the machine's installed capacity, not its
// instantaneous load — momentary pressure is already handled
// continuously by S-26.T2's AdmissionController.Pressure() and S-26.T3's
// ThrottleLadder; recalibrating the static preset on every fluctuation
// in used memory would make an "envelope" flap with load, which
// contradicts the word.
//
// SPORT: internal/fleet/governor.calibrate (ADD, per T-4 sport_updates).
package governor

// Hardware-class memory boundaries. Only the two endpoints are cited
// figures (17-INTAKE-2026-09-03-CHATGPT-V2-MONITORS.md row 22: a 16 GB
// LAN node, a 128 GB workstation); the boundaries themselves are a
// PROVISIONAL midpoint split between those two cited numbers, not
// independently measured.
const (
	// minimalMemCeilingBytes: at or below this, calibrate recommends
	// PresetMinimal. 24 GiB sits above the cited 16 GB node with margin,
	// so a node close to but not exactly 16 GB still classifies minimal.
	minimalMemCeilingBytes = 24 * 1024 * 1024 * 1024
	// performanceMemFloorBytes: at or above this, calibrate recommends
	// PresetPerformance. 64 GiB is half of the cited 128 GB workstation.
	performanceMemFloorBytes = 64 * 1024 * 1024 * 1024
)

// calibrate scores snap against the preset table's hardware-class
// boundaries and returns the recommended preset plus its AdmissionConfig
// and LadderConfig. It never returns an error: a zero-metrics snapshot
// (MemTotalBytes == 0 — no Sampler tick has ever succeeded, or the
// platform is tier-2 and every tick returns ErrUnsupportedPlatform, per
// sampler_windows.go) fails closed to PresetMinimal, the most
// restrictive envelope, rather than treating "no information" as
// license to assume ample hardware. This mirrors AdmissionController's
// own nil-Sampler refusal and ThrottleLadder's own nil-PressureSource
// StageHalt: absence of a signal is never read as permission.
func calibrate(snap ResourceSnapshot) (HardwarePreset, AdmissionConfig, LadderConfig) {
	preset := classify(snap.MemTotalBytes)
	env := presetTable[preset]
	return preset, env.Admission, env.Ladder
}

// classify maps a total-memory reading to the hardware class it implies.
// memTotalBytes == 0 (the zero-metrics case) falls into the same
// minimal branch as genuinely low memory: both mean "do not assume
// ample hardware".
func classify(memTotalBytes uint64) HardwarePreset {
	switch {
	case memTotalBytes <= minimalMemCeilingBytes:
		return PresetMinimal
	case memTotalBytes >= performanceMemFloorBytes:
		return PresetPerformance
	default:
		return PresetBalanced
	}
}

// CalibrationRecommendation is what a future `doctor --fix` surface
// (owned by internal/doctor's composition root, outside this ticket's
// files_scope) shows an operator: calibrate's current recommendation,
// its rationale, and whether a pinned preset would override it. Producing
// this value has no side effect — this package never writes a config
// file itself, in Recommend or anywhere else, which is how "never
// silently overwrite a user-pinned preset" (R-21.215) is satisfied: there
// is no write path to guard, only one to never build.
type CalibrationRecommendation struct {
	// Recommended is the preset calibrate would choose for snap.
	Recommended HardwarePreset
	// Rationale is Recommended's presetTable rationale, verbatim.
	Rationale string
	// Pinned reports whether [governor].preset held an explicit,
	// non-empty value (pinnedRaw) rather than being absent.
	Pinned bool
	// PinnedPreset is the preset that pinnedRaw actually resolves to
	// (fail-closed to PresetMinimal if unrecognised); meaningless when
	// Pinned is false.
	PinnedPreset HardwarePreset
}

// Recommend computes the doctor --fix advisory for the given pinned raw
// [governor].preset TOML value and resource snapshot. It is pure and
// side-effect-free: calling it never changes what Load would return, and
// never touches disk.
func Recommend(pinnedRaw string, snap ResourceSnapshot) CalibrationRecommendation {
	recommended, _, _ := calibrate(snap)
	pinnedPreset, isPinned := resolvePresetName(pinnedRaw)
	return CalibrationRecommendation{
		Recommended:  recommended,
		Rationale:    presetTable[recommended].Rationale,
		Pinned:       isPinned,
		PinnedPreset: pinnedPreset,
	}
}
