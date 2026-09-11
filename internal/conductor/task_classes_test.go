package conductor

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestModelClassToTaskClass asserts every §5.18 ModelClass constant maps
// to the correct TaskClass string.
func TestModelClassToTaskClass(t *testing.T) {
	cases := []struct {
		mc   ModelClass
		want TaskClass
	}{
		{ModelClassMech, TaskClassCode},
		{ModelClassBuild, TaskClassCode},
		{ModelClassHeavy, TaskClassCode},
		{ModelClassReview, TaskClassReview},
		{ModelClassArbiter, TaskClassArbitrate},
	}
	for _, tc := range cases {
		got, err := ModelClassToTaskClass(tc.mc)
		if err != nil {
			t.Fatalf("ModelClassToTaskClass(%q): unexpected error %v", tc.mc, err)
		}
		if got != tc.want {
			t.Errorf("ModelClassToTaskClass(%q) = %q, want %q", tc.mc, got, tc.want)
		}
	}
}

// TestModelClassToTaskClass_FailClosed asserts the zero value and an
// unknown string both return ErrInvalidModelClass, never a permissive
// default.
func TestModelClassToTaskClass_FailClosed(t *testing.T) {
	for _, mc := range []ModelClass{"", "bogus", "MECH", "build "} {
		got, err := ModelClassToTaskClass(mc)
		if !errors.Is(err, ErrInvalidModelClass) {
			t.Errorf("ModelClassToTaskClass(%q): err = %v, want ErrInvalidModelClass", mc, err)
		}
		if got != "" {
			t.Errorf("ModelClassToTaskClass(%q): got %q, want empty on error", mc, got)
		}
		if _, ok := cascade.KindOf(err); !ok {
			t.Errorf("ModelClassToTaskClass(%q): error carries no taxonomy Kind", mc)
		}
	}
}

// TestModelClassToTaskClass_ConsistentWithTaxonomyTable asserts every
// TaskClass ModelClassToTaskClass can return is present in the nine-row
// taskClassTable, so the two normative tables never drift apart.
func TestModelClassToTaskClass_ConsistentWithTaxonomyTable(t *testing.T) {
	names := make(map[string]bool, len(taskClassTable))
	for _, row := range TaskClasses() {
		names[row.Class] = true
	}
	for _, mc := range []ModelClass{ModelClassMech, ModelClassBuild, ModelClassHeavy, ModelClassReview, ModelClassArbiter} {
		tc, err := ModelClassToTaskClass(mc)
		if err != nil {
			t.Fatalf("ModelClassToTaskClass(%q): unexpected error %v", mc, err)
		}
		if !names[string(tc)] {
			t.Errorf("ModelClassToTaskClass(%q) = %q, not present in taskClassTable", mc, tc)
		}
	}
}

// TestTaskClassTable_9Rows asserts the §5.16 table has exactly nine rows
// and every row's SensitivityDefault is a valid SensitivityTier value.
func TestTaskClassTable_9Rows(t *testing.T) {
	rows := TaskClasses()
	if len(rows) != 9 {
		t.Fatalf("TaskClasses(): len = %d, want 9", len(rows))
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Class == "" {
			t.Errorf("row has empty Class")
		}
		if seen[row.Class] {
			t.Errorf("duplicate Class %q in taskClassTable", row.Class)
		}
		seen[row.Class] = true
		if !row.SensitivityDefault.Valid() {
			t.Errorf("row %q: SensitivityDefault %v is not a valid SensitivityTier", row.Class, row.SensitivityDefault)
		}
		if row.CtxK <= 0 {
			t.Errorf("row %q: CtxK = %d, want > 0", row.Class, row.CtxK)
		}
		if row.Reasoning == "" {
			t.Errorf("row %q: empty Reasoning", row.Class)
		}
		if row.LaneAffinity == "" {
			t.Errorf("row %q: empty LaneAffinity", row.Class)
		}
	}
}

// TestTaskClassTable_DefensiveCopy asserts mutating the returned slice
// never affects the package's own table.
func TestTaskClassTable_DefensiveCopy(t *testing.T) {
	rows := TaskClasses()
	rows[0].Class = "mutated"
	again := TaskClasses()
	if again[0].Class == "mutated" {
		t.Fatal("TaskClasses() returned a shared slice, not a defensive copy")
	}
}
