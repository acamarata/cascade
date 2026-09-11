package context

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestComposerErrorConstructorsCarryInvalidInput(t *testing.T) {
	errs := []error{
		errComposerNilCounter(),
		errComposerNilContext(),
		errComposerNonPositiveBudget(0),
		errComposerOversizedSlot(0, SlotKindTier, "gci", 500, 10),
	}
	for _, err := range errs {
		if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
			t.Errorf("error %v: kind = %v (ok=%v), want invalid-input", err, k, ok)
		}
	}
}

func TestComposerOversizedSlotErrorNeverEchoesContent(t *testing.T) {
	secret := "this-is-conversation-content-never-in-an-error"
	err := errComposerOversizedSlot(2, SlotKindHistory, "turn-3", 999, 10)
	if strings.Contains(err.Error(), secret) {
		t.Fatal("errComposerOversizedSlot must never echo slot content")
	}
	// Sanity: the label and kind ARE expected to appear (they are
	// caller-chosen identifiers, never conversation content).
	if !strings.Contains(err.Error(), "turn-3") || !strings.Contains(err.Error(), "history") {
		t.Errorf("error %q: want it to name the label and kind", err.Error())
	}
}

func TestComposerDependencyErrorPreservesKind(t *testing.T) {
	inner := cascade.New(cascade.KindTimeout, "counter timed out")
	wrapped := errComposerDependency(inner, "context: composer: counting")
	k, ok := cascade.KindOf(wrapped)
	if !ok || k != cascade.KindTimeout {
		t.Fatalf("errComposerDependency: kind = %v (ok=%v), want timeout", k, ok)
	}
}

func TestComposerDependencyErrorFallsBackToInternal(t *testing.T) {
	inner := errors.New("plain, untyped failure")
	wrapped := errComposerDependency(inner, "context: composer: counting")
	k, ok := cascade.KindOf(wrapped)
	if !ok || k != cascade.KindInternal {
		t.Fatalf("errComposerDependency: kind = %v (ok=%v), want internal", k, ok)
	}
}
