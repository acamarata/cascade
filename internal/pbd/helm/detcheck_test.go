package helm

import (
	"errors"
	"testing"
)

func TestGoSourcePasses(t *testing.T) {
	if err := CheckDeterministic("main.go"); err != nil {
		t.Fatalf("CheckDeterministic(main.go) = %v, want nil", err)
	}
}

func TestShellScriptFailsM2283(t *testing.T) {
	err := CheckDeterministic("run.sh")
	if err == nil {
		t.Fatal("CheckDeterministic(run.sh) = nil, want M2283")
	}
	if !errors.Is(err, ErrM2283) {
		t.Fatalf("CheckDeterministic(run.sh) = %v, want errors.Is(err, ErrM2283)", err)
	}
}

func TestModelTurnFailsM2283(t *testing.T) {
	err := CheckDeterministic("prompt.txt")
	if err == nil {
		t.Fatal("CheckDeterministic(prompt.txt) = nil, want M2283")
	}
	if !errors.Is(err, ErrM2283) {
		t.Fatalf("CheckDeterministic(prompt.txt) = %v, want errors.Is(err, ErrM2283)", err)
	}
}

func TestEmptyInputFailsClosed(t *testing.T) {
	err := CheckDeterministic("")
	if err == nil {
		t.Fatal("CheckDeterministic(\"\") = nil, want M2283")
	}
	if !errors.Is(err, ErrM2283) {
		t.Fatalf("CheckDeterministic(\"\") = %v, want errors.Is(err, ErrM2283)", err)
	}
}
