package pews

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func mustLoad(t *testing.T, root string) *Tree {
	t.Helper()
	tree, err := NewStore(root, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tree
}

func hasViolation(report Report, kind ViolationKind) bool {
	for _, v := range report.Violations {
		if v.Kind == kind {
			return true
		}
	}
	return false
}

func TestValidate(t *testing.T) {
	t.Run("nil tree refuses", func(t *testing.T) {
		if _, err := Validate(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("err = %v, want KindInvalidInput", err)
		}
	})
	t.Run("clean", testValidateClean)
	t.Run("identity and duplicates", testValidateIdentityAndDuplicates)
	t.Run("gaps and tombstones", testValidateGapsAndTombstones)
	t.Run("dependencies and cycles", testValidateDependenciesAndCycles)
	t.Run("counts", testValidateCounts)
}

func testValidateClean(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
	mkTicketFile(t, root, "N", 3, 28, 2, "P1-E14-W3-S28-T2", []string{"P1-E14-W3-S28-T1"})
	report, err := Validate(mustLoad(t, root))
	if err != nil {
		t.Fatalf("Validate: %v (violations: %+v)", err, report.Violations)
	}
	if !report.OK() || report.ActiveCount != 2 {
		t.Errorf("report = %+v", report)
	}
}

func testValidateIdentityAndDuplicates(t *testing.T) {
	t.Run("identity mismatch is reported and fails closed", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T9", nil)
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationIdentityMismatch) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("duplicate declared id across sprints is reported", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S28-T1", nil)
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationDuplicateID) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("duplicate tombstone entry is reported", func(t *testing.T) {
		root := t.TempDir()
		tomb := "tombstones:\n  - id: P1-E14-W3-S28-T2\n    reason: a\n  - id: P1-E14-W3-S28-T2\n    reason: b\n"
		mustWriteFile(t, filepath.Join(root, "tombstones.yaml"), tomb)
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationDuplicateTomb) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})
}

func testValidateGapsAndTombstones(t *testing.T) {
	t.Run("gap without a tombstone is reported", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		mkTicketFile(t, root, "N", 3, 28, 3, "P1-E14-W3-S28-T3", nil)
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationGap) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("gap covered by a matching tombstone validates clean", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		mkTicketFile(t, root, "N", 3, 28, 3, "P1-E14-W3-S28-T3", nil)
		mustWriteFile(t, filepath.Join(root, "tombstones.yaml"), "tombstones:\n  - id: P1-E14-W3-S28-T2\n    reason: superseded\n")
		report, err := Validate(mustLoad(t, root))
		if err != nil {
			t.Fatalf("Validate: %v (violations: %+v)", err, report.Violations)
		}
		if report.TombstoneCount != 1 {
			t.Errorf("TombstoneCount = %d", report.TombstoneCount)
		}
	})

	t.Run("a live ticket at a tombstoned id is reported", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		mustWriteFile(t, filepath.Join(root, "tombstones.yaml"), "tombstones:\n  - id: P1-E14-W3-S28-T1\n    reason: superseded\n")
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationTombstoneLive) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})
}

func testValidateDependenciesAndCycles(t *testing.T) {
	t.Run("dangling dependency is reported", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", []string{"P1-E99-W1-S01-T1"})
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationDanglingDep) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("dependency on a tombstoned id is reported", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", []string{"P1-E14-W3-S28-T2"})
		mustWriteFile(t, filepath.Join(root, "tombstones.yaml"), "tombstones:\n  - id: P1-E14-W3-S28-T2\n    reason: superseded\n")
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationTombstoneDep) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("a direct cycle is detected", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", []string{"P1-E14-W3-S28-T2"})
		mkTicketFile(t, root, "N", 3, 28, 2, "P1-E14-W3-S28-T2", []string{"P1-E14-W3-S28-T1"})
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationCycle) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})

	t.Run("a self-dependency is a cycle", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", []string{"P1-E14-W3-S28-T1"})
		report, err := Validate(mustLoad(t, root))
		if err == nil || !hasViolation(report, ViolationCycle) {
			t.Errorf("err=%v violations=%+v", err, report.Violations)
		}
	})
}

func testValidateCounts(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
	path := filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-28", "tickets", "T-1.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, path, string(data)+"gate_only: true\n")
	report, err := Validate(mustLoad(t, root))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.GateOnlyCount != 1 {
		t.Errorf("GateOnlyCount = %d", report.GateOnlyCount)
	}
}
