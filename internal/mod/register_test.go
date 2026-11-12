package mod

import (
	"errors"
	"testing"
)

// TestRegisterAllowedGroups verifies that each allowed group registers without error.
func TestRegisterAllowedGroups(t *testing.T) {
	groups := []string{"ci", "ai", "nodes", "jobs", "events", "store", "guard", "tools"}
	for _, g := range groups {
		if err := RegisterPlugin(g, "x"); err != nil {
			t.Errorf("RegisterPlugin(%q, %q) = %v; want nil", g, "x", err)
		}
	}
}

// TestRegisterRejectsUnknownGroup verifies that an unknown group returns ErrGroupNotAllowed.
func TestRegisterRejectsUnknownGroup(t *testing.T) {
	err := RegisterPlugin("billing", "x")
	if !errors.Is(err, ErrGroupNotAllowed) {
		t.Fatalf("RegisterPlugin(\"billing\", \"x\") error = %v; want ErrGroupNotAllowed", err)
	}
	if got := err.Error(); !containsStr(got, "billing") {
		t.Errorf("error message %q does not contain \"billing\"", got)
	}
}

// TestRegisterRejectsFleet verifies that the retired fleet group returns ErrFleetRetired.
func TestRegisterRejectsFleet(t *testing.T) {
	err := RegisterPlugin("fleet", "x")
	if !errors.Is(err, ErrFleetRetired) {
		t.Fatalf("RegisterPlugin(\"fleet\", \"x\") error = %v; want ErrFleetRetired", err)
	}
}

// TestRegisterRejectsCoreVerb verifies that the core group returns ErrCoreVerb.
func TestRegisterRejectsCoreVerb(t *testing.T) {
	err := RegisterPlugin("core", "x")
	if !errors.Is(err, ErrCoreVerb) {
		t.Fatalf("RegisterPlugin(\"core\", \"x\") error = %v; want ErrCoreVerb", err)
	}
}

// TestRegisterRejectsEmpty verifies that empty group or verb returns ErrInvalid.
func TestRegisterRejectsEmpty(t *testing.T) {
	err := RegisterPlugin("", "x")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("RegisterPlugin(\"\", \"x\") error = %v; want ErrInvalid", err)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
