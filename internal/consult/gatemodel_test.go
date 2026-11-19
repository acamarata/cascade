package consult

import (
	"errors"
	"testing"
)

func TestGateModelRoutineIsCheap(t *testing.T) {
	model, err := GateModel("routine")
	if err != nil {
		t.Fatalf("GateModel(\"routine\") returned error: %v", err)
	}
	if model != "haiku" {
		t.Fatalf("GateModel(\"routine\") = %q, want %q", model, "haiku")
	}
	if model == "opus" {
		t.Fatalf("GateModel(\"routine\") = %q, must not be opus", model)
	}
}

func TestGateModelCRQAAreCheap(t *testing.T) {
	for _, kind := range []string{"cr", "qa"} {
		model, err := GateModel(kind)
		if err != nil {
			t.Fatalf("GateModel(%q) returned error: %v", kind, err)
		}
		if model != "haiku" {
			t.Fatalf("GateModel(%q) = %q, want %q", kind, model, "haiku")
		}
		if model == "opus" {
			t.Fatalf("GateModel(%q) = %q, must not be opus", kind, model)
		}
	}
}

func TestGateModelSecurityIsOpus(t *testing.T) {
	model, err := GateModel("security")
	if err != nil {
		t.Fatalf("GateModel(\"security\") returned error: %v", err)
	}
	if model != "opus" {
		t.Fatalf("GateModel(\"security\") = %q, want %q", model, "opus")
	}
}

func TestGateModelHardAndSignoffAreOpus(t *testing.T) {
	for _, kind := range []string{"hard", "gate-signoff"} {
		model, err := GateModel(kind)
		if err != nil {
			t.Fatalf("GateModel(%q) returned error: %v", kind, err)
		}
		if model != "opus" {
			t.Fatalf("GateModel(%q) = %q, want %q", kind, model, "opus")
		}
	}
}

func TestGateModelUnknownKindFailsClosed(t *testing.T) {
	model, err := GateModel("banana")
	if model != "" {
		t.Fatalf("GateModel(\"banana\") = %q, want empty string", model)
	}
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("GateModel(\"banana\") error = %v, want ErrUnknownKind", err)
	}
}
