package conductor

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestResolveSensitivity_ZeroValueIsRestricted(t *testing.T) {
	got := ResolveSensitivity(provider.ModelRequest{})
	if got != provider.SensitivityRestricted {
		t.Fatalf("zero-value sensitivity resolved to %s, want restricted", got)
	}
}

func TestResolveSensitivity_UnresolvableIsRestricted(t *testing.T) {
	got := ResolveSensitivity(provider.ModelRequest{Sensitivity: provider.SensitivityTier(99)})
	if got != provider.SensitivityRestricted {
		t.Fatalf("unresolvable sensitivity resolved to %s, want restricted", got)
	}
}

func TestResolveSensitivity_DeclaredLocalOnlyPassesThrough(t *testing.T) {
	got := ResolveSensitivity(provider.ModelRequest{Sensitivity: provider.SensitivityLocalOnly})
	if got != provider.SensitivityLocalOnly {
		t.Fatalf("declared local-only resolved to %s, want local-only", got)
	}
}

func TestResolveTaskClass_ValidatesNineClassEnum(t *testing.T) {
	for _, name := range []string{"classify", "segment", "summarize", "extract", "chat", "code", "reason", "review", "arbitrate"} {
		if _, err := ResolveTaskClass(provider.ModelRequest{TaskClass: name}); err != nil {
			t.Errorf("ResolveTaskClass(%q): %v", name, err)
		}
	}
}

func TestResolveTaskClass_EmptyIsInvalidRequest(t *testing.T) {
	_, err := ResolveTaskClass(provider.ModelRequest{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty task_class: got %v, want ErrInvalidRequest", err)
	}
}

func TestResolveTaskClass_UnknownIsInvalidRequest(t *testing.T) {
	_, err := ResolveTaskClass(provider.ModelRequest{TaskClass: "triage"})
	if err != ErrInvalidRequest {
		t.Fatalf("unknown task_class %q: got %v, want ErrInvalidRequest", "triage", err)
	}
}

func TestResolveTaskClass_ModelClassNeverAccepted(t *testing.T) {
	// R-16.59: the envelope carries task_class only; a caller that
	// mistakenly sets a model_class-shaped value must be refused, not
	// silently mapped.
	_, err := ResolveTaskClass(provider.ModelRequest{TaskClass: "generate"})
	if err != ErrInvalidRequest {
		t.Fatalf("model_class-shaped value %q: got %v, want ErrInvalidRequest", "generate", err)
	}
}
