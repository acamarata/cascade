package governor

// Purpose: presets.go's test suite. TestPresetsReturnAdmissionConfig is
// this ticket's required check (T-4 checks list) and does three things
// in one pass: (1) proves every enum value resolves to a distinct,
// internally-consistent PresetEnvelope; (2) proves the table's
// LadderConfig entries are already at NormalizeLadderConfig's fixed
// point, guarding against this sprint's own dead-default defect
// (a value declared but never actually applied); (3) greps the package's
// own source for the literal "EnvelopeParams" so R-21.215's strike is a
// machine-asserted fact, not merely a claim in a doc comment.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresetsReturnAdmissionConfig(t *testing.T) {
	presets := []HardwarePreset{PresetMinimal, PresetBalanced, PresetPerformance}
	seen := make(map[AdmissionConfig]HardwarePreset, len(presets))

	for _, p := range presets {
		env, ok := presetTable[p]
		if !ok {
			t.Fatalf("presetTable missing entry for %v", p)
		}
		if env.Admission.CompileClassCap != DefaultCompileClassCap {
			t.Fatalf("%v: CompileClassCap = %d, want fixed %d (06-FORGE-SPEC.md §5 rule 10)",
				p, env.Admission.CompileClassCap, DefaultCompileClassCap)
		}
		if env.Rationale == "" {
			t.Fatalf("%v: Rationale is empty", p)
		}
		if other, dup := seen[env.Admission]; dup {
			t.Fatalf("%v and %v share an identical AdmissionConfig %+v; presets must be distinct",
				p, other, env.Admission)
		}
		seen[env.Admission] = p

		normalized := NormalizeLadderConfig(env.Ladder)
		if normalized != env.Ladder {
			t.Fatalf("%v: LadderConfig %+v is not at NormalizeLadderConfig's fixed point (got %+v) — "+
				"a preset's ladder must already be a valid, applied configuration, never one that "+
				"silently changes the first time it is normalized", p, env.Ladder, normalized)
		}
		if env.Ladder.WarnThreshold > env.Ladder.CriticalThreshold ||
			env.Ladder.CriticalThreshold > env.Ladder.HaltThreshold {
			t.Fatalf("%v: thresholds not ascending: warn=%v critical=%v halt=%v",
				p, env.Ladder.WarnThreshold, env.Ladder.CriticalThreshold, env.Ladder.HaltThreshold)
		}
	}

	if len(seen) != len(presets) {
		t.Fatalf("expected %d distinct AdmissionConfig values, got %d", len(presets), len(seen))
	}
}

func TestPresetBalancedMatchesPackageDefaults(t *testing.T) {
	env := presetTable[PresetBalanced]
	want := AdmissionConfig{
		MaxInflight:     DefaultMaxInflight,
		QueueCap:        DefaultQueueCap,
		CompileClassCap: DefaultCompileClassCap,
		SwapThreshold:   DefaultSwapThreshold,
	}
	if env.Admission != want {
		t.Fatalf("PresetBalanced.Admission = %+v, want the package's own defaults %+v", env.Admission, want)
	}
	wantLadder := LadderConfig{
		WarnThreshold:     DefaultWarnThreshold,
		CriticalThreshold: DefaultCriticalThreshold,
		HaltThreshold:     DefaultHaltThreshold,
		StepDownDwell:     DefaultStepDownDwell,
		PollHz:            DefaultLadderHz,
	}
	if env.Ladder != wantLadder {
		t.Fatalf("PresetBalanced.Ladder = %+v, want the package's own defaults %+v", env.Ladder, wantLadder)
	}
}

func TestHardwarePresetStringFailsClosedOnUnknownValue(t *testing.T) {
	var rogue HardwarePreset = 99
	if got := rogue.String(); got != "minimal" {
		t.Fatalf("HardwarePreset(99).String() = %q, want %q (fail closed)", got, "minimal")
	}
}

// TestNoEnvelopeParamsInPackageSource machine-asserts R-21.215's strike:
// the contract's original EnvelopeParams type must never be declared as a
// Go type or constructed as a struct literal anywhere in this package's
// tracked source. It checks for a small set of exact declaration/
// construction substrings (see bannedForms below) rather than banning the
// bare word outright, because doc comments — including this file's own
// and presets.go's — must be free to name and explain the struck type
// without tripping a self-referential failure.
func TestNoEnvelopeParamsInPackageSource(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	// The struck identifier is built by concatenation, not written as a
	// contiguous literal, so this test file's own source never matches
	// its own scan (mirrors AGENT-BRIEF's credential-fixture-splitting
	// technique for the same reason: a scanner and the text describing
	// it must not collide).
	struckType := "Envelope" + "Params"
	bannedForms := []string{"type " + struckType, struckType + "{", struckType + ")"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == "presets_test.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), err)
		}
		for _, form := range bannedForms {
			if strings.Contains(string(data), form) {
				t.Fatalf("%s contains %q; R-21.215 strikes %s — "+
					"presets must return AdmissionConfig and LadderConfig directly", e.Name(), form, struckType)
			}
		}
	}
}
