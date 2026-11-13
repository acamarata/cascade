package crunch

import (
	"errors"
	"testing"
)

// fakeVerifier is a test implementation of NodeVerifier.
type fakeVerifier struct {
	freeNodes []string
	freeErr   error
	verifyErr error
}

// FreeNodes returns the configured list of free nodes.
func (f *fakeVerifier) FreeNodes() ([]string, error) {
	return f.freeNodes, f.freeErr
}

// Verify simulates command verification against the target.
func (f *fakeVerifier) Verify(target VerifyTarget, commands []string) error {
	return f.verifyErr
}

func TestVerifyWithNodes_NilVerifier(t *testing.T) {
	target, err := VerifyWithNodes([]string{"arg"}, []string{"cmd"}, nil)
	if !errors.Is(err, ErrVerifyNodes) {
		t.Fatalf("expected ErrVerifyNodes, got %v", err)
	}
	if target.Node != "" || target.Local {
		t.Fatalf("expected zero VerifyTarget, got %+v", target)
	}
}

func TestVerifyWithNodes_EmptyCommands(t *testing.T) {
	target, err := VerifyWithNodes([]string{"arg"}, []string{}, &fakeVerifier{})
	if !errors.Is(err, ErrVerifyNodes) {
		t.Fatalf("expected ErrVerifyNodes, got %v", err)
	}
	if target.Node != "" || target.Local {
		t.Fatalf("expected zero VerifyTarget, got %+v", target)
	}
}

func TestVerifyWithNodes_Success(t *testing.T) {
	v := &fakeVerifier{
		freeNodes: []string{"node1"},
		freeErr:   nil,
		verifyErr: nil,
	}
	target, err := VerifyWithNodes([]string{"arg"}, []string{"cmd"}, v)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_ = target
}
