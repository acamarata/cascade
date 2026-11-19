package consult

import "testing"

func TestModelNameLowercaseOk(t *testing.T) {
	if err := ValidateModelName("sonnet-5"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestModelNameUppercaseRejected(t *testing.T) {
	if err := ValidateModelName("Sonnet"); err != ErrBadModelName {
		t.Errorf("expected ErrBadModelName, got %v", err)
	}
}

func TestModelNameLetterDotLetterRejected(t *testing.T) {
	if err := ValidateModelName("son.net"); err != ErrBadModelName {
		t.Errorf("expected ErrBadModelName, got %v", err)
	}
}

func TestModelNameDigitDotDigitOk(t *testing.T) {
	if err := ValidateModelName("model-4.5"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestModelNameLongRunRejected(t *testing.T) {
	if err := ValidateModelName("aaaaaaaaaa"); err != ErrBadModelName {
		t.Errorf("expected ErrBadModelName, got %v", err)
	}
}

func TestModelNameEmptyRejected(t *testing.T) {
	if err := ValidateModelName(""); err != ErrBadModelName {
		t.Errorf("expected ErrBadModelName, got %v", err)
	}
}
