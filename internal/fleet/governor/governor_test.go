package governor

// Purpose: governor.go's test suite. TestGovernorLoadPresetThenOverrides
// is this ticket's required check (T-4 checks list) and covers, in one
// function per this package's existing table-per-scenario style: TOML
// load with an absent [governor] block (safe defaults via calibrate);
// preset applied first then explicit [governor] keys overriding it key
// by key; a user-pinned preset always winning over calibrate output; and
// a second unpinned startup on unchanged hardware being a true no-op.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeGovernorTOML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestGovernorLoadAbsentFileResolvesThroughCalibrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.toml")
	snap := ResourceSnapshot{MemTotalBytes: 32 * 1024 * 1024 * 1024} // balanced range

	admission, ladder, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("Load(absent file) error = %v, want nil", err)
	}
	want := presetTable[PresetBalanced]
	if admission != want.Admission {
		t.Fatalf("Load(absent file).AdmissionConfig = %+v, want the calibrated PresetBalanced entry %+v",
			admission, want.Admission)
	}
	if ladder != want.Ladder {
		t.Fatalf("Load(absent file).LadderConfig = %+v, want the calibrated PresetBalanced entry %+v",
			ladder, want.Ladder)
	}
}

func TestGovernorLoadPresetThenOverrides(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, `
[governor]
preset = "balanced"

[governor.admission]
max_inflight = 99

[governor.ladder]
warn_threshold = 0.10
step_down_dwell = "5s"
`)
	snap := ResourceSnapshot{MemTotalBytes: 128 * 1024 * 1024 * 1024} // would calibrate performance, but preset is pinned

	admission, ladder, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}

	balanced := presetTable[PresetBalanced]

	// Overridden field takes the explicit value.
	if admission.MaxInflight != 99 {
		t.Fatalf("admission.MaxInflight = %d, want 99 (explicit override)", admission.MaxInflight)
	}
	// Every other admission field keeps the pinned preset's baseline.
	if admission.QueueCap != balanced.Admission.QueueCap ||
		admission.CompileClassCap != balanced.Admission.CompileClassCap ||
		admission.SwapThreshold != balanced.Admission.SwapThreshold {
		t.Fatalf("admission = %+v, want the balanced baseline for every field except MaxInflight (%+v)",
			admission, balanced.Admission)
	}

	if ladder.WarnThreshold != 0.10 {
		t.Fatalf("ladder.WarnThreshold = %v, want 0.10 (explicit override)", ladder.WarnThreshold)
	}
	if ladder.StepDownDwell != 5*time.Second {
		t.Fatalf("ladder.StepDownDwell = %v, want 5s (explicit override)", ladder.StepDownDwell)
	}
	// CriticalThreshold/HaltThreshold were not overridden, so they keep
	// the balanced baseline UNLESS NormalizeLadderConfig's ascending-order
	// invariant had to raise them above the lowered WarnThreshold — here
	// 0.10 < DefaultCriticalThreshold(0.80), so no raise is triggered.
	if ladder.CriticalThreshold != balanced.Ladder.CriticalThreshold {
		t.Fatalf("ladder.CriticalThreshold = %v, want the balanced baseline %v",
			ladder.CriticalThreshold, balanced.Ladder.CriticalThreshold)
	}
	if ladder.HaltThreshold != balanced.Ladder.HaltThreshold {
		t.Fatalf("ladder.HaltThreshold = %v, want the balanced baseline %v",
			ladder.HaltThreshold, balanced.Ladder.HaltThreshold)
	}
}

func TestGovernorLoadPinnedPresetWinsOverCalibrate(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, `
[governor]
preset = "minimal"
`)
	// This snapshot would calibrate to PresetPerformance if unpinned.
	snap := ResourceSnapshot{MemTotalBytes: 128 * 1024 * 1024 * 1024}

	admission, ladder, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	want := presetTable[PresetMinimal]
	if admission != want.Admission || ladder != want.Ladder {
		t.Fatalf("Load with preset=\"minimal\" pinned = (%+v, %+v), want PresetMinimal's table entry "+
			"regardless of the 128GB snapshot calibrate would otherwise recommend", admission, ladder)
	}
}

func TestGovernorLoadUnrecognisedPresetFailsClosedToMinimal(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, `
[governor]
preset = "ludicrous-speed"
`)
	snap := ResourceSnapshot{MemTotalBytes: 128 * 1024 * 1024 * 1024}

	admission, ladder, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	want := presetTable[PresetMinimal]
	if admission != want.Admission || ladder != want.Ladder {
		t.Fatalf("Load with an unrecognised preset = (%+v, %+v), want the most restrictive envelope (PresetMinimal), "+
			"never the most permissive", admission, ladder)
	}
}

func TestGovernorLoadSecondUnpinnedStartupIsNoOp(t *testing.T) {
	dir := t.TempDir()
	// Absent [governor] block: unpinned.
	path := writeGovernorTOML(t, dir, "schema_version = 1\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before Load: %v", err)
	}
	snap := ResourceSnapshot{MemTotalBytes: 16 * 1024 * 1024 * 1024}

	a1, l1, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("first Load error = %v, want nil", err)
	}
	a2, l2, err := Load(context.Background(), path, snap)
	if err != nil {
		t.Fatalf("second Load error = %v, want nil", err)
	}

	if a1 != a2 || l1 != l2 {
		t.Fatalf("second unpinned Load on unchanged hardware diverged: first=(%+v,%+v) second=(%+v,%+v)",
			a1, l1, a2, l2)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after Load: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Load rewrote configPath: before=%q after=%q", before, after)
	}
}

func TestGovernorLoadCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "config.toml")

	_, _, err := Load(ctx, path, ResourceSnapshot{})
	if err == nil {
		t.Fatalf("Load with a canceled context returned nil error")
	}
}

func TestGovernorLoadMalformedTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, "[governor\npreset = broken")

	_, _, err := Load(context.Background(), path, ResourceSnapshot{})
	if err == nil {
		t.Fatalf("Load with malformed TOML returned nil error")
	}
}

func TestGovernorLoadUnparseableStepDownDwellReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, `
[governor.ladder]
step_down_dwell = "not-a-duration"
`)

	_, _, err := Load(context.Background(), path, ResourceSnapshot{})
	if err == nil {
		t.Fatalf("Load with an unparseable step_down_dwell returned nil error")
	}
}

func TestGovernorLoadNegativeStepDownDwellIsExplicitNoDwell(t *testing.T) {
	dir := t.TempDir()
	path := writeGovernorTOML(t, dir, `
[governor.ladder]
step_down_dwell = "-1s"
`)

	_, ladder, err := Load(context.Background(), path, ResourceSnapshot{MemTotalBytes: 32 * 1024 * 1024 * 1024})
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	if ladder.StepDownDwell != 0 {
		t.Fatalf("ladder.StepDownDwell = %v, want 0 (negative override flattened to explicit no-dwell "+
			"by NormalizeLadderConfig, called exactly once)", ladder.StepDownDwell)
	}
}
