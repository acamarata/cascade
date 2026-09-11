package context

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNewComposerNilCounter(t *testing.T) {
	c, err := NewComposer(nil, nil)
	if err == nil {
		t.Fatal("NewComposer(nil, nil): want error, got nil")
	}
	if c != nil {
		t.Fatalf("NewComposer(nil, nil): want nil Composer on error, got %+v", c)
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Errorf("NewComposer(nil, nil) error kind = %v (ok=%v), want invalid-input", k, ok)
	}
}

func TestNewComposerValid(t *testing.T) {
	c, err := NewComposer(fixedTokenCounter{n: 1}, nil)
	if err != nil {
		t.Fatalf("NewComposer: unexpected error %v", err)
	}
	if c == nil {
		t.Fatal("NewComposer: want non-nil Composer")
	}
	if c.summarizer != nil {
		t.Error("NewComposer(counter, nil): want nil summarizer stored as-is")
	}
}

func TestNewComposerWithSummarizer(t *testing.T) {
	sg := fakeSummarizer{}
	c, err := NewComposer(fixedTokenCounter{n: 1}, sg)
	if err != nil {
		t.Fatalf("NewComposer: unexpected error %v", err)
	}
	if c.summarizer == nil {
		t.Error("NewComposer(counter, sg): want non-nil summarizer stored")
	}
}
