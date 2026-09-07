// Package governor (throttle_config.go) defines the throttle ladder's
// TOML-configurable tunables and the pure threshold-to-stage mapping.
//
// Purpose: LadderConfig (thresholds + hysteresis window, populated from
//
//	the daemon's [governor] config section, 08-INIT-CONFIG-SPEC.md §3,
//	R-14.42) and NormalizeLadderConfig, which applies this package's
//	"zero-or-negative means default" convention (matching SamplerConfig
//	and AdmissionConfig) and enforces the thresholds' ascending order so
//	a misconfigured ladder still fails closed rather than never escalating.
//
// Inputs: a caller-built LadderConfig, typically decoded from TOML by a
//
//	future config-loader ticket; every field tolerates its zero value.
//
// Outputs: a normalized LadderConfig with every field in range, plus
//
//	pressureToStage's pure mapping from a pressure reading to the stage
//	it implies with no memory of prior stages (hysteresis is applied by
//	the caller, throttle.go's evaluator, never here).
//
// Constraints: pressureToStage must be monotonic non-decreasing in its
//
//	pressure argument for a fixed config - TestPressureToStageMonotonic
//	asserts this directly rather than sampling a few points.
//
// SPORT: internal/fleet/governor.ThrottleLadder (ADD, per T-3
//
//	sport_updates).
package governor

import "time"

// Default tunables for a zero-value LadderConfig field, per
// 08-INIT-CONFIG-SPEC.md §3's "unconfigured -> safe default" contract.
const (
	// DefaultWarnThreshold is LadderConfig.WarnThreshold's default.
	DefaultWarnThreshold = 0.60
	// DefaultCriticalThreshold is LadderConfig.CriticalThreshold's
	// default.
	DefaultCriticalThreshold = 0.80
	// DefaultHaltThreshold is LadderConfig.HaltThreshold's default.
	DefaultHaltThreshold = 0.95
	// DefaultStepDownDwell is LadderConfig.StepDownDwell's default: how
	// long relief must be sustained before the ladder steps down one
	// rung.
	DefaultStepDownDwell = 30 * time.Second
	// DefaultLadderHz is LadderConfig.PollHz's default poll rate.
	DefaultLadderHz = 1.0
	// MaxLadderHz is LadderConfig.PollHz's hard ceiling, mirroring
	// MaxSamplerHz: a configured rate above this is clamped, never
	// rejected.
	MaxLadderHz = 1.0
)

// LadderConfig configures a ThrottleLadder. It is populated from the
// daemon's [governor] config section (08-INIT-CONFIG-SPEC.md §3,
// R-14.42). Every field has a safe default so an absent config block is
// not an error.
type LadderConfig struct {
	// WarnThreshold is the pressure (0-1) at or above which the ladder
	// reports StageWarn. Zero or negative defaults to
	// DefaultWarnThreshold.
	WarnThreshold float64
	// CriticalThreshold is the pressure at or above which the ladder
	// reports StageCritical. Zero or negative defaults to
	// DefaultCriticalThreshold. Raised up to WarnThreshold if configured
	// lower than it (ascending-order invariant).
	CriticalThreshold float64
	// HaltThreshold is the pressure at or above which the ladder reports
	// StageHalt. Zero or negative defaults to DefaultHaltThreshold.
	// Raised up to CriticalThreshold if configured lower than it.
	HaltThreshold float64
	// StepDownDwell is how long relief (pressure implying a lower stage)
	// must be sustained, continuously, before the ladder actually steps
	// down one rung. Zero means unconfigured and defaults to
	// DefaultStepDownDwell, matching the other three fields' "zero means
	// default" convention. A zero-valued field cannot also mean "I
	// really want no dwell", so that intent is expressed with a negative
	// value instead: any negative StepDownDwell normalizes to exactly
	// zero and is preserved as the explicit "no dwell" configuration -
	// step-down then happens on the very next tick that shows relief.
	// Escalation (a worsening reading) is never subject to this dwell -
	// it always applies immediately (R-21.215 fail-closed).
	StepDownDwell time.Duration
	// PollHz is the ladder's maximum tick rate. Zero defaults to
	// DefaultLadderHz; a value above MaxLadderHz is clamped to it.
	PollHz float64
}

// NormalizeLadderConfig returns cfg with every zero-or-negative threshold
// field replaced by its default, PollHz clamped per PeriodForHz's
// contract, StepDownDwell defaulted when zero (unconfigured) and
// flattened to zero when negative (explicitly no dwell), and the three
// thresholds forced into ascending order (Warn <= Critical <= Halt) by
// raising a lower-configured stage up to the one below it. A caller that
// configures thresholds out of order gets a ladder that is at least as
// restrictive as configured, never one that silently skips a stage on
// the way up.
func NormalizeLadderConfig(cfg LadderConfig) LadderConfig {
	if cfg.WarnThreshold <= 0 {
		cfg.WarnThreshold = DefaultWarnThreshold
	}
	if cfg.CriticalThreshold <= 0 {
		cfg.CriticalThreshold = DefaultCriticalThreshold
	}
	if cfg.HaltThreshold <= 0 {
		cfg.HaltThreshold = DefaultHaltThreshold
	}
	if cfg.CriticalThreshold < cfg.WarnThreshold {
		cfg.CriticalThreshold = cfg.WarnThreshold
	}
	if cfg.HaltThreshold < cfg.CriticalThreshold {
		cfg.HaltThreshold = cfg.CriticalThreshold
	}
	switch {
	case cfg.StepDownDwell < 0:
		cfg.StepDownDwell = 0
	case cfg.StepDownDwell == 0:
		cfg.StepDownDwell = DefaultStepDownDwell
	}
	return cfg
}

// ladderPeriodForHz converts a configured rate into a tick interval,
// applying LadderConfig.PollHz's default-and-clamp rule. Mirrors
// sampler.go's PeriodForHz exactly but stays local to this file so the
// ladder's rate ceiling can diverge from the sampler's in the future
// without renegotiating a shared export.
func ladderPeriodForHz(hz float64) time.Duration {
	switch {
	case hz <= 0:
		hz = DefaultLadderHz
	case hz > MaxLadderHz:
		hz = MaxLadderHz
	}
	return time.Duration(float64(time.Second) / hz)
}

// pressureToStage maps a single pressure reading to the stage it implies
// under cfg's thresholds, with no memory of any prior stage. It is a
// pure function: hysteresis lives entirely in throttle.go's evaluator,
// which calls this once per tick to get the "target" stage before
// deciding whether to actually move toward it.
func pressureToStage(pressure float64, cfg LadderConfig) ThrottleStage {
	switch {
	case pressure >= cfg.HaltThreshold:
		return StageHalt
	case pressure >= cfg.CriticalThreshold:
		return StageCritical
	case pressure >= cfg.WarnThreshold:
		return StageWarn
	default:
		return StageNormal
	}
}
