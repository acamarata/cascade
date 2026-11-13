package background

import (
	"testing"
)

// TestPortfolioDecisions verifies Decisions returns a defensive copy or empty slice.
func TestPortfolioDecisions(t *testing.T) {
	var nilP *Portfolio
	got := nilP.Decisions()
	if got == nil {
		t.Fatal("nil receiver: expected non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("nil receiver: expected empty slice, got %v", got)
	}

	zero := Portfolio{}
	got = zero.Decisions()
	if got == nil {
		t.Fatal("zero value: expected non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("zero value: expected empty slice, got %v", got)
	}

	p := Portfolio{DecisionValues: []string{"a", "b", "a"}}
	got = p.Decisions()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "a" {
		t.Fatalf("populated: unexpected %v", got)
	}

	got[0] = "z"
	if p.DecisionValues[0] != "a" {
		t.Fatal("defensive copy failed: mutation affected backing field")
	}
}

// TestPortfolioStatus verifies Status returns stored value or idle.
func TestPortfolioStatus(t *testing.T) {
	var nilP *Portfolio
	if got := nilP.Status(); got != "idle" {
		t.Fatalf("nil receiver: expected idle, got %q", got)
	}

	zero := Portfolio{}
	if got := zero.Status(); got != "idle" {
		t.Fatalf("zero value: expected idle, got %q", got)
	}

	p := Portfolio{StatusValue: "active"}
	if got := p.Status(); got != "active" {
		t.Fatalf("populated: expected active, got %q", got)
	}
}

// TestPortfolioTick verifies Tick returns stored value or 0.
func TestPortfolioTick(t *testing.T) {
	var nilP *Portfolio
	if got := nilP.Tick(); got != 0 {
		t.Fatalf("nil receiver: expected 0, got %d", got)
	}

	zero := Portfolio{}
	if got := zero.Tick(); got != 0 {
		t.Fatalf("zero value: expected 0, got %d", got)
	}

	p := Portfolio{TickValue: 42}
	if got := p.Tick(); got != 42 {
		t.Fatalf("populated: expected 42, got %d", got)
	}
}

// TestPortfolioReplay verifies Replay returns stored value or false.
func TestPortfolioReplay(t *testing.T) {
	var nilP *Portfolio
	if got := nilP.Replay(); got != false {
		t.Fatalf("nil receiver: expected false, got %v", got)
	}

	zero := Portfolio{}
	if got := zero.Replay(); got != false {
		t.Fatalf("zero value: expected false, got %v", got)
	}

	p := Portfolio{ReplayValue: true}
	if got := p.Replay(); got != true {
		t.Fatalf("populated: expected true, got %v", got)
	}
}
