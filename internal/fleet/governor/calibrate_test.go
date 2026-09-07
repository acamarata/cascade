package governor

// Purpose: calibrate.go's test suite — table-driven preset selection
// over seeded ResourceSnapshot fixtures, the zero-metrics fail-closed
// path, and Recommend's advisory (never mutates anything, pin detection).

import "testing"

func TestCalibrateSelectsExpectedPreset(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	cases := []struct {
		name string
		snap ResourceSnapshot
		want HardwarePreset
	}{
		{
			name: "16GB LAN node (17-INTAKE row 22 cited minimal target)",
			snap: ResourceSnapshot{MemTotalBytes: 16 * gib},
			want: PresetMinimal,
		},
		{
			name: "at the minimal ceiling exactly",
			snap: ResourceSnapshot{MemTotalBytes: minimalMemCeilingBytes},
			want: PresetMinimal,
		},
		{
			name: "32GB mid-range host",
			snap: ResourceSnapshot{MemTotalBytes: 32 * gib},
			want: PresetBalanced,
		},
		{
			name: "at the performance floor exactly",
			snap: ResourceSnapshot{MemTotalBytes: performanceMemFloorBytes},
			want: PresetPerformance,
		},
		{
			name: "128GB workstation (17-INTAKE row 22 cited performance target)",
			snap: ResourceSnapshot{MemTotalBytes: 128 * gib},
			want: PresetPerformance,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preset, admission, ladder := calibrate(tc.snap)
			if preset != tc.want {
				t.Fatalf("calibrate(%+v) preset = %v, want %v", tc.snap, preset, tc.want)
			}
			wantEnv := presetTable[tc.want]
			if admission != wantEnv.Admission {
				t.Fatalf("calibrate(%+v) admission = %+v, want %+v", tc.snap, admission, wantEnv.Admission)
			}
			if ladder != wantEnv.Ladder {
				t.Fatalf("calibrate(%+v) ladder = %+v, want %+v", tc.snap, ladder, wantEnv.Ladder)
			}
		})
	}
}

// TestCalibrateZeroMetricsFailsClosedToMinimal is this ticket's required
// error-path test: a zero-metrics snapshot (no Sampler tick has ever
// succeeded, or the platform is tier-2 per sampler_windows.go) must
// yield a documented safe default, never a panic and never an error —
// calibrate has no error return at all.
func TestCalibrateZeroMetricsFailsClosedToMinimal(t *testing.T) {
	preset, admission, ladder := calibrate(ResourceSnapshot{})
	if preset != PresetMinimal {
		t.Fatalf("calibrate(zero snapshot) preset = %v, want PresetMinimal (fail closed on missing information)", preset)
	}
	want := presetTable[PresetMinimal]
	if admission != want.Admission || ladder != want.Ladder {
		t.Fatalf("calibrate(zero snapshot) = (%+v, %+v), want PresetMinimal's table entry", admission, ladder)
	}
}

func TestCalibrateSecondCallOnUnchangedSnapshotIsPureConvergence(t *testing.T) {
	snap := ResourceSnapshot{MemTotalBytes: 32 * 1024 * 1024 * 1024}
	p1, a1, l1 := calibrate(snap)
	p2, a2, l2 := calibrate(snap)
	if p1 != p2 || a1 != a2 || l1 != l2 {
		t.Fatalf("calibrate is not pure: first=(%v,%+v,%+v) second=(%v,%+v,%+v)", p1, a1, l1, p2, a2, l2)
	}
}

func TestRecommendReportsCalibrationAndPinState(t *testing.T) {
	snap := ResourceSnapshot{MemTotalBytes: 128 * 1024 * 1024 * 1024}

	unpinned := Recommend("", snap)
	if unpinned.Pinned {
		t.Fatalf("Recommend(\"\", ...).Pinned = true, want false")
	}
	if unpinned.Recommended != PresetPerformance {
		t.Fatalf("Recommend(\"\", ...).Recommended = %v, want PresetPerformance", unpinned.Recommended)
	}
	if unpinned.Rationale == "" {
		t.Fatalf("Recommend(\"\", ...).Rationale is empty")
	}

	pinned := Recommend("minimal", snap)
	if !pinned.Pinned || pinned.PinnedPreset != PresetMinimal {
		t.Fatalf("Recommend(\"minimal\", ...) = %+v, want Pinned=true PinnedPreset=PresetMinimal", pinned)
	}
	if pinned.Recommended != PresetPerformance {
		t.Fatalf("Recommend's Recommended must still reflect calibrate(snap) regardless of pin state, got %v",
			pinned.Recommended)
	}

	unrecognised := Recommend("typo", snap)
	if !unrecognised.Pinned || unrecognised.PinnedPreset != PresetMinimal {
		t.Fatalf("Recommend(\"typo\", ...) = %+v, want Pinned=true PinnedPreset=PresetMinimal (fail closed)",
			unrecognised)
	}
}
