package repo

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func fixtureGraphForReachability() *SymbolGraph {
	return &SymbolGraph{
		Nodes: []GraphNode{
			{ID: "app", Kind: NodePackage, Package: "app", File: "app/main.go"},
			{ID: "app#Run", Kind: NodeFunc, Name: "Run", Package: "app", File: "app/main.go"},
			{ID: "internal/policy", Kind: NodePackage, Package: "internal/policy", File: "internal/policy/policy.go"},
			{ID: "internal/policy#Check", Kind: NodeFunc, Name: "Check", Package: "internal/policy", File: "internal/policy/policy.go"},
			{ID: "internal/other", Kind: NodePackage, Package: "internal/other", File: "internal/other/other.go"},
		},
		Edges: []GraphEdge{
			{From: "app", To: "app#Run", Kind: EdgeDeclares},
			{From: "app#Run", To: "internal/policy", Kind: EdgeCalls},
			{From: "internal/policy", To: "internal/policy#Check", Kind: EdgeDeclares},
			{From: "internal/policy", To: "internal/other", Kind: EdgeImports},
			// A real cycle: internal/other imports back to app.
			{From: "internal/other", To: "app", Kind: EdgeImports},
		},
	}
}

func TestReachable_FindsSensitivePackageAcrossHops(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	got, err := r.Reachable(context.Background(), []string{"app/main.go"}, []SensitiveClass{ClassAuth})
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	want := map[string]bool{"internal/policy": true, "internal/policy#Check": true}
	if len(got) != len(want) {
		t.Fatalf("Reachable = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("Reachable returned unexpected id %q", id)
		}
	}
}

// TestReachable_RealCycleDoesNotHang proves the visited-set guard: the
// fixture graph above has a real cycle (other -> app -> Run -> policy ->
// other), and Reachable must terminate rather than loop.
func TestReachable_RealCycleDoesNotHang(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	done := make(chan struct{})
	var got []string
	go func() {
		got, _ = r.Reachable(context.Background(), []string{"internal/other/other.go"}, []SensitiveClass{ClassAuth})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Reachable did not terminate on a cyclic graph")
	}
	want := map[string]bool{"internal/policy": true, "internal/policy#Check": true}
	if len(got) != len(want) {
		t.Errorf("Reachable across the cycle = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("Reachable across the cycle returned unexpected id %q", id)
		}
	}
}

func TestReachable_NoneOfPathsResolve_IsNotFound(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	_, err = r.Reachable(context.Background(), []string{"never/declared.go"}, []SensitiveClass{ClassAuth})
	if err == nil {
		t.Fatal("Reachable with an unresolvable path set = nil error, want KindNotFound")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("Reachable unresolvable path error kind = %v, ok=%v, want KindNotFound", kind, ok)
	}
}

func TestReachable_ResolvedButNothingSensitive_IsEmptySuccess(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	got, err := r.Reachable(context.Background(), []string{"internal/other/other.go"}, []SensitiveClass{ClassSecret})
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if got != nil {
		t.Errorf("Reachable resolved-but-nothing-sensitive = %v, want nil/empty", got)
	}
}

func TestReachable_EmptyClassesIsInvalidInput(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	_, err = r.Reachable(context.Background(), []string{"app/main.go"}, nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("Reachable with no classes: kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestReachable_UnknownClassIsInvalidInput(t *testing.T) {
	r, err := NewReachability(fixtureGraphForReachability())
	if err != nil {
		t.Fatalf("NewReachability: %v", err)
	}
	_, err = r.Reachable(context.Background(), []string{"app/main.go"}, []SensitiveClass{"bogus"})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("Reachable with unknown class: kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestNewReachability_NilGraphIsInvalidInput(t *testing.T) {
	_, err := NewReachability(nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("NewReachability(nil): kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestSensitiveClass_Valid(t *testing.T) {
	for _, c := range []SensitiveClass{ClassAuth, ClassSecret, ClassSchema} {
		if !c.Valid() {
			t.Errorf("%q should be a valid sensitive class", string(c))
		}
	}
	if SensitiveClass("bogus").Valid() {
		t.Error("an undeclared class should not validate")
	}
}
