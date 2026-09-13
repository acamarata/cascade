package economics

import (
	"errors"
	"testing"
)

func TestModesWrittenOrder(t *testing.T) {
	want := []Mode{ModeDiscover, ModePlan, ModeBuild, ModeCrunch, ModeIntegrate, ModeVerify, ModeRelease, ModeIncident}
	got := Modes()
	if len(got) != len(want) {
		t.Fatalf("Modes() = %d entries, want %d", len(got), len(want))
	}
	for i, m := range want {
		if got[i] != m {
			t.Errorf("Modes()[%d] = %q, want %q", i, got[i], m)
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, m := range Modes() {
		got, err := ParseMode(string(m))
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", m, err)
		}
		if got != m {
			t.Errorf("ParseMode(%q) = %q, want %q", m, got, m)
		}
	}
}

func TestParseModeUnknown(t *testing.T) {
	for _, bad := range []string{"", "PLAN", "planning", "hyperdrive"} {
		if _, err := ParseMode(bad); !errors.Is(err, ErrUnknownMode) {
			t.Errorf("ParseMode(%q) error = %v, want ErrUnknownMode", bad, err)
		}
	}
}

// TestModeForLifecycleStageTotal asserts the R-21.32 + R-21.49 mapping
// stage by stage, against a Go literal typed independently from
// 21-T0-RULINGS-R21.md's own text (never derived from lifecycleStageMode
// itself) -- the exact "assert against the spec, not a copy of itself"
// discipline this phase repeatedly needed.
func TestModeForLifecycleStageTotal(t *testing.T) {
	cases := map[string]Mode{
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
	if len(cases) != 13 {
		t.Fatalf("test literal has %d stages, want 13 (AH/S-69.T1)", len(cases))
	}
	for stage, want := range cases {
		got, err := ModeForLifecycleStage(stage)
		if err != nil {
			t.Fatalf("ModeForLifecycleStage(%q): %v", stage, err)
		}
		if got != want {
			t.Errorf("ModeForLifecycleStage(%q) = %q, want %q", stage, got, want)
		}
	}
}

func TestModeForLifecycleStageUnknown(t *testing.T) {
	for _, bad := range []string{"", "unknown_stage", "INTENT", "review"} {
		if _, err := ModeForLifecycleStage(bad); !errors.Is(err, ErrUnknownLifecycleStage) {
			t.Errorf("ModeForLifecycleStage(%q) error = %v, want ErrUnknownLifecycleStage", bad, err)
		}
	}
}

func TestModeValid(t *testing.T) {
	for _, m := range Modes() {
		if !m.Valid() {
			t.Errorf("Mode %q should be valid", m)
		}
	}
	if Mode("").Valid() {
		t.Error("zero-value Mode should be invalid")
	}
	if Mode("crunch-mode").Valid() {
		t.Error("unknown Mode should be invalid")
	}
}
