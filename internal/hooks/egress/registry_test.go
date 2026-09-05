package egress

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegistryRefusesUnknownAndDuplicate covers the registration error
// paths: an empty identifier, a missing owner, an unknown tier in
// AllowedTiers, and a second registration of one class.
func TestRegistryRefusesUnknownAndDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", InterceptConfig{Enabled: true, Owner: "t"}); err == nil {
		t.Fatal("an empty class identifier was accepted")
	}
	if err := r.Register("a.class", InterceptConfig{Enabled: true}); err == nil {
		t.Fatal("a class with no owner was accepted")
	}
	bad := InterceptConfig{Enabled: true, Owner: "t", AllowedTiers: []SensitivityTier{"secret"}}
	if err := r.Register("b.class", bad); err == nil {
		t.Fatal("a class listing an unknown tier was accepted")
	}
	if err := r.Register("c.class", InterceptConfig{Enabled: true, Owner: "t"}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := r.Register("c.class", InterceptConfig{Enabled: true, Owner: "other"})
	if !errors.Is(err, ErrDuplicateClass) {
		t.Fatalf("duplicate registration: got %v, want ErrDuplicateClass", err)
	}
	if cfg, ok := r.Lookup("c.class"); !ok || cfg.Owner != "t" {
		t.Fatalf("the duplicate overwrote the first registration: %+v ok=%v", cfg, ok)
	}
}

// TestRegistryMustRegisterPanics proves the init-time registration path
// stops the process rather than continuing with an ambiguous policy.
func TestRegistryMustRegisterPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister on a duplicate did not panic")
		}
	}()
	r := NewRegistry()
	r.MustRegister("dup", InterceptConfig{Enabled: true, Owner: "t"})
	r.MustRegister("dup", InterceptConfig{Enabled: true, Owner: "t"})
}

// TestRegistryClassesSorted pins the deterministic listing order.
func TestRegistryClassesSorted(t *testing.T) {
	r := NewRegistry()
	for _, name := range []EgressClass{"zed", "alpha", "mid"} {
		r.MustRegister(name, InterceptConfig{Enabled: true, Owner: "t"})
	}
	got := r.Classes()
	want := []EgressClass{"alpha", "mid", "zed"}
	if len(got) != len(want) {
		t.Fatalf("Classes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Classes() = %v, want %v", got, want)
		}
	}
}

// TestCapabilityIsUnforgeable proves the only route to a usable
// capability is the registry: the zero value names no class, and an
// unknown or disabled class yields nothing to hold.
func TestCapabilityIsUnforgeable(t *testing.T) {
	if (Capability{}).Class() != "" {
		t.Fatal("the zero Capability names a class")
	}
	r := NewRegistry()
	r.MustRegister("open", InterceptConfig{Enabled: true, Owner: "t"})
	r.MustRegister("shut", InterceptConfig{Enabled: false, Owner: "t"})

	token, err := r.Capability("open")
	if err != nil || token.Class() != "open" {
		t.Fatalf("Capability(open) = %v, %v", token, err)
	}
	if _, err := r.Capability("absent"); !errors.Is(err, ErrUnknownClass) {
		t.Fatalf("Capability(absent) = %v, want ErrUnknownClass", err)
	} else if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Capability(absent) kind = %v, want not-found", err)
	}
	if _, err := r.Capability("shut"); !errors.Is(err, ErrClassDisabled) {
		t.Fatalf("Capability(shut) = %v, want ErrClassDisabled", err)
	}
}
