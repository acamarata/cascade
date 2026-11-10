package executive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type kindsGolden struct {
	Groups []struct {
		Group KindGroup `json:"group"`
		Kinds []Kind    `json:"kinds"`
	} `json:"groups"`
}

func loadKindsGolden(t *testing.T) kindsGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "exec-kinds.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g kindsGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	return g
}

// TestEXE01LegacyAC1 verifies AllKinds is exactly the 41-kind R-21.37 list,
// in golden order, and GroupOf returns each kind's golden group.
func TestEXE01LegacyAC1(t *testing.T) {
	g := loadKindsGolden(t)
	var want []Kind
	for _, grp := range g.Groups {
		for _, k := range grp.Kinds {
			want = append(want, k)
			got, err := GroupOf(k)
			if err != nil || got != grp.Group {
				t.Errorf("GroupOf(%q) = %q, %v; want %q", k, got, err, grp.Group)
			}
		}
	}
	if len(want) != 41 || len(AllKinds) != 41 {
		t.Fatalf("golden has %d kinds, AllKinds %d; want 41", len(want), len(AllKinds))
	}
	seen := map[Kind]bool{}
	for i, k := range AllKinds {
		if k != want[i] {
			t.Errorf("AllKinds[%d] = %q, want %q", i, k, want[i])
		}
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
	if KindTaskStarted != "exec.TASK_STARTED" || KindT0WakeRequested != "exec.T0_WAKE_REQUESTED" {
		t.Errorf("Go names do not map to R-21.37 values: %q %q", KindTaskStarted, KindT0WakeRequested)
	}
	if _, err := GroupOf("exec.TASK_UNKNOWN"); !errors.Is(err, ErrUnknownExecKind) {
		t.Errorf("GroupOf(unknown) err = %v, want ErrUnknownExecKind", err)
	}
}

// TestEXE01LegacyAC2 verifies ParseExecKind returns ErrUnknownExecKind for unset or unknown kinds.
func TestEXE01LegacyAC2(t *testing.T) {
	cases := []struct {
		input string
		want  error
	}{
		{"", ErrUnknownExecKind},
		{"exec.unknown", ErrUnknownExecKind},
		{"invalid", ErrUnknownExecKind},
		{"exec.task.started", ErrUnknownExecKind},
		{"TASK_STARTED", ErrUnknownExecKind},
		{"exec.TASK_UNKNOWN", ErrUnknownExecKind},
	}

	for _, tc := range cases {
		_, err := ParseExecKind(tc.input)
		if !errors.Is(err, tc.want) {
			t.Errorf("ParseExecKind(%q) error = %v, want %v", tc.input, err, tc.want)
		}
	}
	for _, k := range AllKinds {
		got, err := ParseExecKind(string(k))
		if err != nil || got != k {
			t.Errorf("ParseExecKind(%q) = %q, %v; want itself", k, got, err)
		}
	}
}
