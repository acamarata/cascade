package consult

import (
	"errors"
	"testing"
)

func TestBlockedUnknownUse(t *testing.T) {
	cases := []string{
		"dependency update",
		"   ",
		" dependency update ",
		"Dependency Update",
		"UNKNOWN",
	}
	for _, use := range cases {
		t.Run(use, func(t *testing.T) {
			log, err := ValidateNativeTokenUse(use, nil)
			if err == nil {
				t.Fatalf("expected error for use %q", use)
			}
			var pe *PolicyError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *PolicyError, got %T", err)
			}
			if pe.Code != "D4652" {
				t.Errorf("expected code D4652, got %s", pe.Code)
			}
			if pe.Use != use {
				t.Errorf("expected use %q, got %q", use, pe.Use)
			}
			if !errors.Is(err, ErrNativeTokenUseNotAllowed) {
				t.Errorf("expected errors.Is to match ErrNativeTokenUseNotAllowed")
			}
			expectedText := "D4652: native token use not allowed: " + use
			if err.Error() != expectedText {
				t.Errorf("expected error text %q, got %q", expectedText, err.Error())
			}
			if len(log) != 1 {
				t.Fatalf("expected 1 violation, got %d", len(log))
			}
			if log[0].Code != "D4652" || log[0].Use != use {
				t.Errorf("unexpected violation record: %+v", log[0])
			}
		})
	}
}

func TestBlockedEmptyUse(t *testing.T) {
	log, err := ValidateNativeTokenUse("", nil)
	if err == nil {
		t.Fatal("expected error for empty use")
	}
	var pe *PolicyError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PolicyError, got %T", err)
	}
	if pe.Code != "D4652" {
		t.Errorf("expected code D4652, got %s", pe.Code)
	}
	if pe.Use != "" {
		t.Errorf("expected use to be empty, got %q", pe.Use)
	}
	expectedText := "D4652: native token use not allowed: "
	if err.Error() != expectedText {
		t.Errorf("expected error text %q, got %q", expectedText, err.Error())
	}
	if len(log) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(log))
	}
	if log[0].Code != "D4652" || log[0].Use != "" {
		t.Errorf("unexpected violation record: %+v", log[0])
	}
}

func TestViolationRecorded(t *testing.T) {
	history := []NativeTokenViolation{
		{Code: "OLD", Use: "old-use"},
	}

	backing := make([]NativeTokenViolation, 1, 10)
	backing[0] = history[0]

	use := "dependency update"
	log, err := ValidateNativeTokenUse(use, backing)
	if err == nil {
		t.Fatal("expected error")
	}

	if len(log) != 2 {
		t.Fatalf("expected 2 violations, got %d", len(log))
	}
	if log[0].Code != "OLD" || log[0].Use != "old-use" {
		t.Errorf("history modified: %+v", log[0])
	}
	if log[1].Code != "D4652" || log[1].Use != use {
		t.Errorf("expected new violation, got %+v", log[1])
	}

	backingFull := backing[:10]
	if backingFull[1].Code == "D4652" && backingFull[1].Use == use {
		t.Errorf("input backing array was mutated")
	}

	log2, _ := ValidateNativeTokenUse("another bad use", nil)
	if len(log2) != 1 || log2[0].Code != "D4652" || log2[0].Use != "another bad use" {
		t.Errorf("nil log did not work: %+v", log2)
	}

	log3, _ := ValidateNativeTokenUse("use1", nil)
	log4, _ := ValidateNativeTokenUse("use2", log3)
	if len(log4) != 2 {
		t.Errorf("expected 2 violations, got %d", len(log4))
	}
}
