package pbd

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

func TestIDViolationKindsSet(t *testing.T) {
	want := []pews.ViolationKind{
		pews.ViolationIdentityMismatch,
		pews.ViolationDuplicateID,
		pews.ViolationGap,
		pews.ViolationTombstoneLive,
		pews.ViolationDuplicateTomb,
	}
	for _, k := range want {
		if !idViolationKinds[k] {
			t.Errorf("idViolationKinds missing %q", k)
		}
	}
	if len(idViolationKinds) != len(want) {
		t.Errorf("idViolationKinds has %d entries, want exactly %d", len(idViolationKinds), len(want))
	}
	if depViolationKinds[pews.ViolationDuplicateID] {
		t.Error("duplicate-id must not also be a dependency-class kind")
	}
}

func TestFormatIDViolation(t *testing.T) {
	t.Run("with path", func(t *testing.T) {
		v := pews.Violation{Kind: pews.ViolationDuplicateID, TicketID: "P1-E14-W3-S28-T1", Path: "epics/E-N/.../T-1.yaml", Message: "declared twice"}
		got := formatIDViolation(v)
		if !strings.Contains(got, string(v.Kind)) || !strings.Contains(got, v.Message) || !strings.Contains(got, v.Path) {
			t.Errorf("formatIDViolation = %q, missing kind/message/path", got)
		}
	})

	t.Run("without path", func(t *testing.T) {
		v := pews.Violation{Kind: pews.ViolationGap, TicketID: "P1-E14-W3-S28-T2", Message: "missing"}
		got := formatIDViolation(v)
		if !strings.Contains(got, string(v.Kind)) || !strings.Contains(got, v.Message) {
			t.Errorf("formatIDViolation = %q, missing kind/message", got)
		}
		if strings.Contains(got, "()") {
			t.Errorf("formatIDViolation = %q, should not render an empty path suffix", got)
		}
	})
}
