package pbd

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

func TestDepViolationKindsSet(t *testing.T) {
	want := []pews.ViolationKind{
		pews.ViolationDanglingDep,
		pews.ViolationTombstoneDep,
		pews.ViolationCycle,
	}
	for _, k := range want {
		if !depViolationKinds[k] {
			t.Errorf("depViolationKinds missing %q", k)
		}
	}
	if len(depViolationKinds) != len(want) {
		t.Errorf("depViolationKinds has %d entries, want exactly %d", len(depViolationKinds), len(want))
	}
	if idViolationKinds[pews.ViolationCycle] {
		t.Error("dependency-cycle must not also be an id-class kind")
	}
}

func TestFormatDepViolation(t *testing.T) {
	t.Run("with path", func(t *testing.T) {
		v := pews.Violation{Kind: pews.ViolationDanglingDep, TicketID: "P1-E14-W3-S28-T1", Path: "epics/E-N/.../T-1.yaml", Message: "unknown target"}
		got := formatDepViolation(v)
		if !strings.Contains(got, string(v.Kind)) || !strings.Contains(got, v.Message) || !strings.Contains(got, v.Path) {
			t.Errorf("formatDepViolation = %q, missing kind/message/path", got)
		}
	})

	t.Run("without path", func(t *testing.T) {
		v := pews.Violation{Kind: pews.ViolationCycle, TicketID: "P1-E14-W3-S28-T1", Message: "A -> B -> A"}
		got := formatDepViolation(v)
		if !strings.Contains(got, string(v.Kind)) || !strings.Contains(got, v.Message) {
			t.Errorf("formatDepViolation = %q, missing kind/message", got)
		}
	})
}
