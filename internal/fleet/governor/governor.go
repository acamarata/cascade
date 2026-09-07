// Package governor (governor.go) is the governor config loader: it reads
// the [governor] TOML section (08-INIT-CONFIG-SPEC.md §3, R-14.42) and
// resolves it to the two live types S-26.T2 and S-26.T3 both read their
// configuration from (P1-E13-W3-S26-T4).
//
// Purpose: Load parses [governor].preset, [governor.admission], and
//
//	[governor.ladder], applies the selected preset FIRST (presets.go's
//	table, calibrated via calibrate.go when unpinned), then overrides it
//	key by key with whatever [governor] keys are explicitly present
//	(R-21.215). This is the only write-free path in the package: Load
//	never mutates configPath, in any branch, which is how the "second
//	unpinned startup on unchanged hardware performs no config rewrite"
//	requirement is met — not by detecting "no change" and skipping a
//	write, but by never having a write path to begin with. A second call
//	with the same configPath and the same snapshot is pure convergence
//	because calibrate (calibrate.go) is a pure function of its snapshot
//	argument.
//
// Inputs: a config file path (read via os.ReadFile; a missing file is
//
//	not an error — it resolves through the same "absent [governor]
//	block" path as a present-but-empty one) and a ResourceSnapshot
//	(typically *Sampler.Snapshot(), passed by value so Load itself needs
//	no injected Clock and stays a pure function of its two arguments).
//
// Outputs: (AdmissionConfig, LadderConfig, error). The error return is
//
//	reserved for a malformed [governor] TOML block or an unparseable
//	step_down_dwell duration string — never for an absent file, an absent
//	block, or an unrecognised preset name, all three of which resolve to
//	a documented default rather than a failure.
//
// Constraints: no bare time.Now (Load takes its snapshot as a parameter,
//
//	never samples anything itself). DOUBLE-NORMALIZATION AVOIDANCE:
//	presetTable's LadderConfig entries are already valid, ascending,
//	positive-or-explicitly-negative values (presets_test.go asserts
//	this), so NormalizeLadderConfig is called exactly ONCE here, on the
//	fully preset-then-overridden result — never on the preset alone and
//	never a second time after that. Calling it earlier and later both
//	would risk re-applying a default over a value the first pass already
//	resolved, which is the exact defect S-26.T3's own
//	DefaultStepDownDwell dead-code bug taught this package to guard
//	against.
//
// SPORT: internal/fleet/governor.Load (ADD, per T-4 sport_updates).
package governor

import (
	"context"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

// governorFileTOML is the raw TOML file shape Load decodes: only the
// [governor] table is read; every other 08 §3 section is ignored here,
// matching config_load.go's own "sections this ticket does not own are
// preserved, never validated" policy for whichever ticket eventually
// merges this loader into the shared runtime.Config tree.
type governorFileTOML struct {
	Governor governorTOML `toml:"governor"`
}

// governorTOML is the raw [governor] table (08-INIT-CONFIG-SPEC.md §3).
// sampler_hz is deliberately not decoded here: it configures S-26.T1's
// Sampler, a different construction step from the (AdmissionConfig,
// LadderConfig) pair this ticket's Load returns, and reading it here
// without a caller to hand it to would be exactly the kind of
// declared-but-unapplied value this ticket was warned against.
type governorTOML struct {
	Preset    string                `toml:"preset"`
	Admission governorAdmissionTOML `toml:"admission"`
	Ladder    governorLadderTOML    `toml:"ladder"`
}

// governorAdmissionTOML is [governor.admission]. A zero field means
// "not present in this file" — the same "zero means unconfigured"
// convention AdmissionConfig's own doc comments already establish, so
// overriding by a plain positive-value check never has to reinvent it.
type governorAdmissionTOML struct {
	QueueCap        int     `toml:"queue_cap"`
	MaxInflight     int     `toml:"max_inflight"`
	CompileClassCap int     `toml:"compile_class_cap"`
	SwapThreshold   float64 `toml:"swap_threshold"`
}

// governorLadderTOML is [governor.ladder]. StepDownDwell is decoded as a
// raw duration string (e.g. "30s", "-1s") because go-toml/v2 has no
// built-in time.Duration decoding; time.ParseDuration runs it through
// applyLadderOverrides, so a negative string still reaches
// NormalizeLadderConfig's documented negative-means-explicit-no-dwell
// escape hatch unchanged.
type governorLadderTOML struct {
	WarnThreshold     float64 `toml:"warn_threshold"`
	CriticalThreshold float64 `toml:"critical_threshold"`
	HaltThreshold     float64 `toml:"halt_threshold"`
	StepDownDwell     string  `toml:"step_down_dwell"`
	PollHz            float64 `toml:"poll_hz"`
}

// Load reads the [governor] TOML section from configPath and resolves it
// against snap, returning the AdmissionConfig and LadderConfig S-26.T2
// and S-26.T3 both consume. See the file-level doc for the full
// preset-then-override precedence and why this function never writes.
func Load(ctx context.Context, configPath string, snap ResourceSnapshot) (AdmissionConfig, LadderConfig, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionConfig{}, LadderConfig{}, cascade.Wrap(cascade.KindCanceled, err, "governor: load canceled")
	}

	raw, err := readGovernorTOML(configPath)
	if err != nil {
		return AdmissionConfig{}, LadderConfig{}, err
	}

	baseline := resolveBaseline(raw.Preset, snap)
	admission := applyAdmissionOverrides(baseline.Admission, raw.Admission)
	ladder, err := applyLadderOverrides(baseline.Ladder, raw.Ladder)
	if err != nil {
		return AdmissionConfig{}, LadderConfig{}, err
	}
	ladder = NormalizeLadderConfig(ladder)

	return admission, ladder, nil
}

// readGovernorTOML reads and decodes configPath's [governor] table. A
// missing file resolves to the zero governorTOML (absent [governor]
// block), not an error: 08-INIT-CONFIG-SPEC.md §3 guarantees every
// [governor] key has a safe default, so a daemon's very first run
// (before `cascade init` has ever written a config file) must load
// cleanly.
func readGovernorTOML(configPath string) (governorTOML, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return governorTOML{}, nil
		}
		return governorTOML{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: read config file")
	}
	var file governorFileTOML
	if err := toml.Unmarshal(data, &file); err != nil {
		return governorTOML{}, cascade.Wrap(cascade.KindInvalidInput, err, "governor: parse [governor] section")
	}
	return file.Governor, nil
}

// resolvePresetName interprets a [governor].preset TOML value.
// "" (absent) is unpinned: pinned=false, and the returned preset value
// is meaningless — callers must calibrate instead of reading it. Any of
// the three preset names is pinned to that exact preset. Anything else
// non-empty is pinned but unrecognised, and FAILS CLOSED to
// PresetMinimal — the most restrictive envelope, never the most
// permissive — matching this package's established convention (a nil
// Sampler refuses every Admit; a nil PressureSource reports StageHalt
// unconditionally).
func resolvePresetName(name string) (preset HardwarePreset, pinned bool) {
	switch name {
	case "":
		return PresetBalanced, false
	case PresetMinimal.String():
		return PresetMinimal, true
	case PresetBalanced.String():
		return PresetBalanced, true
	case PresetPerformance.String():
		return PresetPerformance, true
	default:
		return PresetMinimal, true
	}
}

// resolveBaseline picks the PresetEnvelope Load applies before any
// [governor] key override: the pinned preset's table entry when
// presetName is pinned, or calibrate's recommendation for snap when it
// is not. A user-pinned preset always wins over calibrate's
// recommendation — snap is not even read in the pinned branch.
func resolveBaseline(presetName string, snap ResourceSnapshot) PresetEnvelope {
	preset, pinned := resolvePresetName(presetName)
	if !pinned {
		calibrated, admission, ladder := calibrate(snap)
		return PresetEnvelope{
			Admission: admission,
			Ladder:    ladder,
			Rationale: presetTable[calibrated].Rationale,
		}
	}
	return presetTable[preset]
}

// applyAdmissionOverrides overrides base field by field with every
// present (nonzero) [governor.admission] key, leaving the preset's value
// wherever a key was absent from configPath.
func applyAdmissionOverrides(base AdmissionConfig, overlay governorAdmissionTOML) AdmissionConfig {
	if overlay.QueueCap > 0 {
		base.QueueCap = overlay.QueueCap
	}
	if overlay.MaxInflight > 0 {
		base.MaxInflight = overlay.MaxInflight
	}
	if overlay.CompileClassCap > 0 {
		base.CompileClassCap = overlay.CompileClassCap
	}
	if overlay.SwapThreshold > 0 {
		base.SwapThreshold = overlay.SwapThreshold
	}
	return base
}

// applyLadderOverrides overrides base field by field with every present
// [governor.ladder] key. step_down_dwell is the one field whose presence
// test is "non-empty string" rather than "positive number", since an
// explicit negative duration is itself a meaningful override (see the
// governorLadderTOML doc comment).
func applyLadderOverrides(base LadderConfig, overlay governorLadderTOML) (LadderConfig, error) {
	if overlay.WarnThreshold > 0 {
		base.WarnThreshold = overlay.WarnThreshold
	}
	if overlay.CriticalThreshold > 0 {
		base.CriticalThreshold = overlay.CriticalThreshold
	}
	if overlay.HaltThreshold > 0 {
		base.HaltThreshold = overlay.HaltThreshold
	}
	if overlay.StepDownDwell != "" {
		d, err := time.ParseDuration(overlay.StepDownDwell)
		if err != nil {
			return LadderConfig{}, cascade.Wrap(cascade.KindInvalidInput, err,
				"governor: parse [governor.ladder].step_down_dwell")
		}
		base.StepDownDwell = d
	}
	if overlay.PollHz > 0 {
		base.PollHz = overlay.PollHz
	}
	return base, nil
}
