package background

import (
	"testing"
)

func TestSubmitOpsToolReturnsD0665(t *testing.T) {
	err := SubmitLaneName("opstool")
	if err != ErrD0665 {
		t.Errorf("expected ErrD0665, got %v", err)
	}
}

func TestSubmitDecideReturnsD0665(t *testing.T) {
	err := SubmitLaneName("decide")
	if err != ErrD0665 {
		t.Errorf("expected ErrD0665, got %v", err)
	}
}

func TestSubmitValidNameAccepted(t *testing.T) {
	err := SubmitLaneName("analyze")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestSubmitEmptyNameRejected(t *testing.T) {
	err := SubmitLaneName("")
	if err != ErrEmptyName {
		t.Errorf("expected ErrEmptyName, got %v", err)
	}
}
