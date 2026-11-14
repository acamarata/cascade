package frontier

import (
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestFrontierOrdersByChainThenDependents verifies sort order.
func TestFrontierOrdersByChainThenDependents(t *testing.T) {
	candidates := []Ticket{
		{ID: "a", Protected: false, Dependents: []string{"x"}},
		{ID: "b", Protected: false, Dependents: []string{"x", "y"}},
		{ID: "c", Protected: false, Dependents: []string{}},
	}
	edges := map[string][]string{
		"a": {"c"},
		"b": {},
		"c": {},
	}
	f := &Frontier{}
	ready, err := f.Ready(candidates, edges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 3 {
		t.Fatalf("expected 3 tickets, got %d", len(ready))
	}
	// LongestPath counts the ticket itself: a=2, b=1 (2 dependents), c=1 (0 dependents).
	// Order: a (path=2), b (path=1, deps=2), c (path=1, deps=0)
	if ready[0].ID != "a" || ready[1].ID != "b" || ready[2].ID != "c" {
		t.Errorf("unexpected order: %s, %s, %s", ready[0].ID, ready[1].ID, ready[2].ID)
	}
}

// TestFrontierSkipsProtected verifies protected tickets are excluded.
func TestFrontierSkipsProtected(t *testing.T) {
	candidates := []Ticket{
		{ID: "a", Protected: true, Dependents: []string{}},
		{ID: "b", Protected: false, Dependents: []string{}},
	}
	edges := map[string][]string{}
	f := &Frontier{}
	ready, err := f.Ready(candidates, edges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "b" {
		t.Errorf("expected only ticket b, got %v", ready)
	}
}

// TestFrontierUnder100msOn5000Tickets verifies performance with 5000 tickets.
func TestFrontierUnder100msOn5000Tickets(t *testing.T) {
	n := 5000
	candidates := make([]Ticket, n)
	edges := make(map[string][]string)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%d", i)
		deps := []string{}
		if i+1 < n {
			deps = append(deps, fmt.Sprintf("t%d", i+1))
		}
		candidates[i] = Ticket{ID: id, Protected: false, Dependents: deps}
		edges[id] = deps
	}
	f := &Frontier{}
	clock := runtime.NewSystemClock()
	start := clock.Now()
	ready, err := f.Ready(candidates, edges)
	elapsed := clock.Now().Sub(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Errorf("took %v, expected under 100ms", elapsed)
	}
	if len(ready) != n || ready[0].ID != "t0" || ready[n-1].ID != fmt.Sprintf("t%d", n-1) {
		t.Errorf("expected %d tickets from t0 to t%d, got %d", n, n-1, len(ready))
	}
}

// TestFrontierChainSurvivesACycle verifies Ready skips cyclic tickets and
// still ranks the acyclic ones. The name carries the Frontier subject because
// chain_test.go already declares TestChainSurvivesACycle for LongestPath.
func TestFrontierChainSurvivesACycle(t *testing.T) {
	candidates := []Ticket{
		{ID: "A", Dependents: []string{"B"}},
		{ID: "B", Dependents: []string{"C"}},
		{ID: "D", Dependents: []string{"E"}},
		{ID: "E"},
	}
	edges := map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": {"A"},
		"D": {"E"},
		"E": {},
	}
	f := &Frontier{}
	ready, err := f.Ready(candidates, edges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 2 || ready[0].ID != "D" || ready[1].ID != "E" {
		t.Fatalf("expected [D E] with cyclic A and B skipped, got %v", ready)
	}
}
