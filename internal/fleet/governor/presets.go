// Package governor (presets.go) defines the governor's three built-in
// hardware-envelope presets (P1-E13-W3-S26-T4).
//
// Purpose: HardwarePreset (minimal/balanced/performance) and the preset
//
//	table mapping each value to a real AdmissionConfig (S-26.T2's exact
//	type) and LadderConfig (S-26.T3's exact type) — R-21.215 strikes the
//	contract's original EnvelopeParams: there is no second, differently
//	named copy of the admission knobs.
//
// Inputs: none; the table is a fixed set of literal values.
//
// Outputs: presetTable, keyed by HardwarePreset, read by calibrate.go and
//
//	governor.go. PresetBalanced is verbatim this package's own Default*
//	constants (admission_types.go, throttle_config.go) — real, already
//	shipped values, not invented for this ticket. PresetMinimal and
//	PresetPerformance are a documented, conservative/permissive multiplier
//	of that same baseline, calibrated toward the two hardware fitness
//	targets 17-INTAKE-2026-09-03-CHATGPT-V2-MONITORS.md row 22 cites (a
//	16 GB LAN node, a 128 GB workstation) — no measured throughput curve
//	against either exists in this corpus, so the multiplier magnitude
//	itself is marked PROVISIONAL in the field comments below rather than
//	presented as a calibrated figure (Art.2). CompileClassCap is fixed at
//	1 across all three: 06-FORGE-SPEC.md §5 rule 10 ("one heavy compile
//	per repo per session") is a correctness ceiling, not a resource-scaling
//	knob.
//
// Constraints: every field here must be read by calibrate.go/governor.go
//
//	(no declared-but-unapplied default — this ticket's own contract names
//	the ladder's DefaultStepDownDwell dead-code defect as the reason this
//	rule exists). An unrecognised preset name never resolves through this
//	table directly; governor.go's resolvePresetName fails it closed to
//	PresetMinimal before this table is even consulted.
//
// SPORT: internal/fleet/governor.HardwarePreset (ADD, per T-4
//
//	sport_updates).
package governor

import "time"

// HardwarePreset names one of the governor's three built-in hardware
// envelopes. The zero value is PresetMinimal, the most restrictive —
// consistent with this package's fail-closed convention (a zero-value
// AdmissionController Sampler and a zero-value ThrottleLadder
// PressureSource are both treated as "refuse", never "allow").
type HardwarePreset int

const (
	// PresetMinimal is calibrated toward a low-memory host (the 16 GB
	// LAN CI/clean-room node 17-INTAKE row 22 cites): fewer inflight
	// slots, a smaller queue, and a lower swap/pressure tolerance so the
	// governor backs off before a fragile host starts swapping heavily.
	PresetMinimal HardwarePreset = iota
	// PresetBalanced is this package's own already-shipped "unconfigured
	// -> safe default" values (S-26.T2/S-26.T3's Default* constants),
	// unchanged.
	PresetBalanced
	// PresetPerformance is calibrated toward a high-memory host (the
	// 128 GB workstation 17-INTAKE row 22 cites): more inflight slots, a
	// larger queue, and a higher swap/pressure tolerance, since ample
	// memory means the pressure signal should not trip until later.
	PresetPerformance
)

// String renders p as the exact TOML value [governor].preset accepts
// (08-INIT-CONFIG-SPEC.md §3). Every HardwarePreset value this package
// defines has a case here; a value outside that set (which production
// code can only construct by ignoring the exported constants) renders as
// "minimal" — the same fail-closed choice resolvePresetName makes for an
// unrecognised TOML string.
func (p HardwarePreset) String() string {
	switch p {
	case PresetBalanced:
		return "balanced"
	case PresetPerformance:
		return "performance"
	case PresetMinimal:
		return "minimal"
	default:
		return "minimal"
	}
}

// PresetEnvelope bundles the AdmissionConfig and LadderConfig one named
// preset resolves to, plus the documented reason for its values — shown
// verbatim by Recommend's doctor-facing output.
type PresetEnvelope struct {
	// Admission is the preset's AdmissionConfig, S-26.T2's exact type.
	Admission AdmissionConfig
	// Ladder is the preset's LadderConfig, S-26.T3's exact type.
	Ladder LadderConfig
	// Rationale is a one-paragraph, human-readable justification for
	// this preset's values, including provenance for every number that
	// is not this package's own already-shipped default.
	Rationale string
}

// presetTable is the governor's fixed set of built-in hardware envelopes,
// keyed by HardwarePreset. Every field is a real, applied value — nothing
// here is declared without a caller: calibrate.go and governor.go are
// this table's only readers, and presets_test.go's
// TestPresetsReturnAdmissionConfig asserts every entry is exercised and
// internally consistent (already in NormalizeLadderConfig's fixed point,
// ascending thresholds, CompileClassCap pinned to 1).
var presetTable = map[HardwarePreset]PresetEnvelope{
	PresetMinimal: {
		Admission: AdmissionConfig{
			MaxInflight:     DefaultMaxInflight / 2, // 2: halved, mirroring effectiveMaxInflight's own halve-with-floor-1 convention (admission.go)
			QueueCap:        DefaultQueueCap / 2,    // 32: halved — less pending work buffered in memory on a fragile host
			CompileClassCap: DefaultCompileClassCap, // 1: fixed (06-FORGE-SPEC.md §5 rule 10), never scales with hardware
			SwapThreshold:   0.35,                   // PROVISIONAL: below DefaultSwapThreshold(0.50) by a conservative margin; no measured swap-onset curve for the cited 16 GB node exists yet
		},
		Ladder: LadderConfig{
			WarnThreshold:     0.45,                     // PROVISIONAL: DefaultWarnThreshold(0.60) minus a 0.15 conservative margin
			CriticalThreshold: 0.65,                     // PROVISIONAL: DefaultCriticalThreshold(0.80) minus the same 0.15 margin
			HaltThreshold:     0.85,                     // PROVISIONAL: DefaultHaltThreshold(0.95) minus the same 0.15 margin
			StepDownDwell:     2 * DefaultStepDownDwell, // 60s: PROVISIONAL, doubled so a fragile host is slower to relax
			PollHz:            DefaultLadderHz,
		},
		Rationale: "minimal: calibrated toward the 16 GB LAN CI/clean-room node " +
			"17-INTAKE-2026-09-03-CHATGPT-V2-MONITORS.md row 22 cites as the governor's " +
			"low-resource hardware fitness target. MaxInflight and QueueCap are halved " +
			"from this package's own already-shipped defaults; SwapThreshold and the " +
			"ladder thresholds are lowered by a documented conservative margin and " +
			"StepDownDwell is doubled. The margin magnitude is provisional: no measured " +
			"benchmark against real 16 GB hardware exists in this corpus (Art.2). " +
			"CompileClassCap stays fixed at 1 (06-FORGE-SPEC.md §5 rule 10).",
	},
	PresetBalanced: {
		Admission: AdmissionConfig{
			MaxInflight:     DefaultMaxInflight,
			QueueCap:        DefaultQueueCap,
			CompileClassCap: DefaultCompileClassCap,
			SwapThreshold:   DefaultSwapThreshold,
		},
		Ladder: LadderConfig{
			WarnThreshold:     DefaultWarnThreshold,
			CriticalThreshold: DefaultCriticalThreshold,
			HaltThreshold:     DefaultHaltThreshold,
			StepDownDwell:     DefaultStepDownDwell,
			PollHz:            DefaultLadderHz,
		},
		Rationale: "balanced: this package's own already-shipped 'unconfigured -> " +
			"safe default' values (admission_types.go, throttle_config.go) — real, " +
			"unchanged for this ticket, not invented.",
	},
	PresetPerformance: {
		Admission: AdmissionConfig{
			MaxInflight:     DefaultMaxInflight * 2, // 8: PROVISIONAL, doubled
			QueueCap:        DefaultQueueCap * 2,    // 128: PROVISIONAL, doubled
			CompileClassCap: DefaultCompileClassCap, // 1: fixed (06-FORGE-SPEC.md §5 rule 10), never scales with hardware
			SwapThreshold:   0.65,                   // PROVISIONAL: above DefaultSwapThreshold(0.50) by a conservative margin
		},
		Ladder: LadderConfig{
			WarnThreshold:     0.70,             // PROVISIONAL: DefaultWarnThreshold(0.60) plus a permissive margin
			CriticalThreshold: 0.85,             // PROVISIONAL: DefaultCriticalThreshold(0.80) plus a permissive margin
			HaltThreshold:     0.97,             // PROVISIONAL: DefaultHaltThreshold(0.95) plus a permissive margin
			StepDownDwell:     15 * time.Second, // PROVISIONAL: halved, ample headroom recovers faster
			PollHz:            DefaultLadderHz,
		},
		Rationale: "performance: calibrated toward the 128 GB workstation " +
			"17-INTAKE-2026-09-03-CHATGPT-V2-MONITORS.md row 22 cites as the governor's " +
			"high-resource hardware fitness target. MaxInflight and QueueCap are " +
			"doubled from this package's own already-shipped defaults; SwapThreshold " +
			"and the ladder thresholds are raised by a documented permissive margin and " +
			"StepDownDwell is halved. The margin magnitude is provisional: no measured " +
			"benchmark against real 128 GB hardware exists in this corpus (Art.2). " +
			"CompileClassCap stays fixed at 1 (06-FORGE-SPEC.md §5 rule 10).",
	},
}
