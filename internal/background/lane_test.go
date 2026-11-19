package background

import (
	"testing"
)

func TestSubmitOpsToolReturnsD0665(t *testing.T) {
	err := Submit("opstool")
	if err != ErrD0665 {
		t.Errorf("expected ErrD0665, got %v", err)
	}
}

func TestSubmitDecideReturnsD0665(t *testing.T) {
	err := Submit("decide")
	if err != ErrD0665 {
		t.Errorf("expected ErrD0665, got %v", err)
	}
}

func TestSubmitValidNameAccepted(t *testing.T) {
	err := Submit("analyze")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestSubmitEmptyNameRejected(t *testing.T) {
	err := Submit("")
	if err != ErrEmptyName {
		t.Errorf("expected ErrEmptyName, got %v", err)
	}
}
