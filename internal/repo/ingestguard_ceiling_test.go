package repo

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestIngestGuardFileCountCeiling(t *testing.T) {
	ceiling := 100

	if err := EnforceCeiling(50, ceiling); err != nil {
		t.Fatalf("below ceiling: unexpected error: %v", err)
	}
	if err := EnforceCeiling(ceiling, ceiling); err != nil {
		t.Fatalf("at ceiling: unexpected error: %v", err)
	}
	err := EnforceCeiling(ceiling+1, ceiling)
	if err == nil {
		t.Fatal("above ceiling: expected error, got nil")
	}
	k, ok := cascade.KindOf(err)
	if !ok || k != cascade.KindPolicyDenied {
		t.Fatalf("above ceiling: want KindPolicyDenied, got %v (ok=%v)", k, ok)
	}
}

func TestEnforceCeilingRefusesAboveDefault(t *testing.T) {
	if err := EnforceCeiling(DefaultFileCountCeiling, DefaultFileCountCeiling); err != nil {
		t.Fatalf("at default ceiling: unexpected error: %v", err)
	}
	err := EnforceCeiling(DefaultFileCountCeiling+1, DefaultFileCountCeiling)
	if err == nil {
		t.Fatal("above default ceiling: expected error, got nil")
	}
	k, ok := cascade.KindOf(err)
	if !ok || k != cascade.KindPolicyDenied {
		t.Fatalf("above default ceiling: want KindPolicyDenied, got %v (ok=%v)", k, ok)
	}
}
