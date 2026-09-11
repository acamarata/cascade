package jobs

import (
	"context"
	"errors"
	"testing"
)

// TestTemplateRegistry_Completeness asserts By resolves exactly the six
// DECIDED kinds and nothing else -- the registry-completeness
// acceptance criterion.
func TestTemplateRegistry_Completeness(t *testing.T) {
	kinds := []string{
		TemplateKindImplement, TemplateKindReview, TemplateKindAdversarial,
		TemplateKindQA, TemplateKindCI, TemplateKindIntegrate,
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatalf("By(%q): unexpected error %v", kind, err)
			}
			if tmpl == nil {
				t.Fatalf("By(%q): nil template, nil error", kind)
			}
		})
	}
}

// TestTemplateRegistry_UnknownKind asserts By returns the typed
// ErrUnknownTemplateKind for any kind outside the six, never a panic
// and never a (nil, nil) return.
func TestTemplateRegistry_UnknownKind(t *testing.T) {
	for _, kind := range []string{"", "bogus", "IMPLEMENT", "review "} {
		tmpl, err := TemplateRegistry.By(kind)
		if !errors.Is(err, ErrUnknownTemplateKind) {
			t.Fatalf("By(%q): got err %v, want ErrUnknownTemplateKind", kind, err)
		}
		if tmpl != nil {
			t.Fatalf("By(%q): got non-nil template alongside an error", kind)
		}
	}
}

// TestTemplateRegistry_RegisterIsAdditive asserts Register can add a
// brand-new kind without disturbing the six already registered --
// AH/S-69.T2's own extension seam.
func TestTemplateRegistry_RegisterIsAdditive(t *testing.T) {
	TemplateRegistry.Register("probe-kind-for-test", ReviewTemplate{})
	defer func() {
		TemplateRegistry.mu.Lock()
		delete(TemplateRegistry.kinds, "probe-kind-for-test")
		TemplateRegistry.mu.Unlock()
	}()

	if _, err := TemplateRegistry.By("probe-kind-for-test"); err != nil {
		t.Fatalf("By(probe-kind-for-test): %v", err)
	}
	if _, err := TemplateRegistry.By(TemplateKindImplement); err != nil {
		t.Fatalf("registering a new kind disturbed an existing one: %v", err)
	}
}

// TestRequireTemplateContext_NilContext asserts the nil-context error
// path: Resolve must refuse rather than panic on ctx.Value.
func TestRequireTemplateContext_NilContext(t *testing.T) {
	tmpl := NewImplementTemplate(nil)
	//nolint:staticcheck // deliberately passing a nil context.Context to exercise the guard.
	_, err := tmpl.Resolve(nil)
	if err == nil {
		t.Fatal("Resolve(nil): expected an error, got nil")
	}
}

// TestRequireTemplateContext_MissingContext asserts a ctx with no
// attached TemplateContext also refuses, rather than resolving a
// zero-value DagNode.
func TestRequireTemplateContext_MissingContext(t *testing.T) {
	tmpl := ReviewTemplate{}
	_, err := tmpl.Resolve(context.Background())
	if err == nil {
		t.Fatal("Resolve with no TemplateContext: expected an error, got nil")
	}
}

// TestRequireTemplateContext_EmptyID asserts a TemplateContext with no
// id also refuses.
func TestRequireTemplateContext_EmptyID(t *testing.T) {
	ctx := WithTemplateContext(context.Background(), TemplateContext{})
	if _, err := (QaTemplate{}).Resolve(ctx); err == nil {
		t.Fatal("Resolve with empty TemplateContext.ID: expected an error, got nil")
	}
}

// TestMergeCapabilities asserts the shared helper de-duplicates and
// preserves base order.
func TestMergeCapabilities(t *testing.T) {
	got := mergeCapabilities([]string{"a", "b"}, "b", "c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("mergeCapabilities: got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("mergeCapabilities: got %v, want %v", got, want)
		}
	}
}
