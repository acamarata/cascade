package consult

import (
	"errors"
	"testing"
)

func TestSaveRoutingPreference(t *testing.T) {
	// zero-value memory initializes
	var m ContextMemory
	err := m.SaveRoutingPreference("ctx1", RoutingPreference{Class: "c1", Model: "m1"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	p, err := m.LoadRoutingPreference("ctx1")
	if err != nil {
		t.Fatalf("expected no error on load, got %v", err)
	}
	if p.Class != "c1" || p.Model != "m1" {
		t.Fatalf("expected c1/m1, got %v/%v", p.Class, p.Model)
	}

	// repeated ctxID overwrites atomically
	err = m.SaveRoutingPreference("ctx1", RoutingPreference{Class: "c2", Model: "m2"})
	if err != nil {
		t.Fatalf("expected no error on overwrite, got %v", err)
	}
	p, err = m.LoadRoutingPreference("ctx1")
	if err != nil {
		t.Fatalf("expected no error on load, got %v", err)
	}
	if p.Class != "c2" || p.Model != "m2" {
		t.Fatalf("expected c2/m2, got %v/%v", p.Class, p.Model)
	}

	// blank ctxID yields ErrInvalidRoutingPreference and preserves state
	err = m.SaveRoutingPreference(" ", RoutingPreference{Class: "c3", Model: "m3"})
	if !errors.Is(err, ErrInvalidRoutingPreference) {
		t.Fatalf("expected ErrInvalidRoutingPreference, got %v", err)
	}
	p, _ = m.LoadRoutingPreference("ctx1")
	if p.Class != "c2" || p.Model != "m2" {
		t.Fatalf("state mutated on invalid save: %v", p)
	}

	// blank Class yields ErrInvalidRoutingPreference
	err = m.SaveRoutingPreference("ctx2", RoutingPreference{Class: " ", Model: "m3"})
	if !errors.Is(err, ErrInvalidRoutingPreference) {
		t.Fatalf("expected ErrInvalidRoutingPreference for blank Class, got %v", err)
	}

	// blank Model yields ErrInvalidRoutingPreference
	err = m.SaveRoutingPreference("ctx2", RoutingPreference{Class: "c3", Model: " "})
	if !errors.Is(err, ErrInvalidRoutingPreference) {
		t.Fatalf("expected ErrInvalidRoutingPreference for blank Model, got %v", err)
	}

	// nil receiver yields ErrInvalidContextMemory
	var nilM *ContextMemory
	err = nilM.SaveRoutingPreference("ctx3", RoutingPreference{Class: "c4", Model: "m4"})
	if !errors.Is(err, ErrInvalidContextMemory) {
		t.Fatalf("expected ErrInvalidContextMemory, got %v", err)
	}
}

func TestLoadRoutingPreference(t *testing.T) {
	var m ContextMemory
	m.SaveRoutingPreference("ctx1", RoutingPreference{Class: "c1", Model: "m1"})

	// exact saved bytes survive Load
	p, err := m.LoadRoutingPreference("ctx1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.Class != "c1" || p.Model != "m1" {
		t.Fatalf("expected c1/m1, got %v/%v", p.Class, p.Model)
	}

	// blank ctxID yields zero preference and ErrInvalidRoutingPreference
	p, err = m.LoadRoutingPreference(" ")
	if !errors.Is(err, ErrInvalidRoutingPreference) {
		t.Fatalf("expected ErrInvalidRoutingPreference, got %v", err)
	}
	if p.Class != "" || p.Model != "" {
		t.Fatalf("expected zero preference, got %v", p)
	}

	// nil receiver yields ErrInvalidContextMemory
	var nilM *ContextMemory
	p, err = nilM.LoadRoutingPreference("ctx1")
	if !errors.Is(err, ErrInvalidContextMemory) {
		t.Fatalf("expected ErrInvalidContextMemory, got %v", err)
	}
	if p.Class != "" || p.Model != "" {
		t.Fatalf("expected zero preference, got %v", p)
	}
}

func TestLoadMissingPreferenceFailsClosed(t *testing.T) {
	var m ContextMemory
	m.SaveRoutingPreference("ctx1", RoutingPreference{Class: "c1", Model: "m1"})

	// absent valid ctxID yields zero preference and ErrNoRoutingPreference
	p, err := m.LoadRoutingPreference("ctxMissing")
	if !errors.Is(err, ErrNoRoutingPreference) {
		t.Fatalf("expected ErrNoRoutingPreference, got %v", err)
	}
	if p.Class != "" || p.Model != "" {
		t.Fatalf("expected zero preference, got %v", p)
	}

	// nil receiver yields zero preference and ErrInvalidContextMemory
	var nilM *ContextMemory
	p, err = nilM.LoadRoutingPreference("ctxMissing")
	if !errors.Is(err, ErrInvalidContextMemory) {
		t.Fatalf("expected ErrInvalidContextMemory, got %v", err)
	}
	if p.Class != "" || p.Model != "" {
		t.Fatalf("expected zero preference, got %v", p)
	}
}
