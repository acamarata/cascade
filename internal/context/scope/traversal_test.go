package scope

import "testing"

// TestTraversalTableGolden pins the whole closed traversal table so a
// later widening (a new permitted pair, or a changed Direction/Transitive
// value) fails CI rather than silently drifting (R-21.157).
func TestTraversalTableGolden(t *testing.T) {
	golden := map[ScopeKind]map[EdgeClass]TraversalRule{
		ScopeKindSession: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
		},
		ScopeKindTask: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
		},
		ScopeKindProject: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
		},
		ScopeKindWorkspace: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
		},
		ScopeKindProduct: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
			EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
		},
		ScopeKindGlobal: {
			EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		},
	}
	allKinds := []ScopeKind{ScopeKindSession, ScopeKindTask, ScopeKindProject, ScopeKindWorkspace, ScopeKindProduct, ScopeKindGlobal, ScopeKindGeneral}
	allEdges := []EdgeClass{EdgeClassParent, EdgeClassMember, EdgeClassRoute}
	for _, k := range allKinds {
		for _, e := range allEdges {
			want, wantOK := golden[k][e]
			got, gotOK := Traversal(k, e)
			if gotOK != wantOK {
				t.Errorf("Traversal(%q, %q) ok = %v, want %v", k, e, gotOK, wantOK)
				continue
			}
			if gotOK && got != want {
				t.Errorf("Traversal(%q, %q) = %+v, want %+v", k, e, got, want)
			}
		}
	}
}

// TestTraversalUnlistedPairDenies asserts the no-permissive-default rule
// directly: ScopeKindGeneral (which has no entry in the table at all) and
// an unknown ScopeKind/EdgeClass both deny rather than falling back to any
// default rule.
func TestTraversalUnlistedPairDenies(t *testing.T) {
	cases := []struct {
		kind ScopeKind
		edge EdgeClass
	}{
		{ScopeKindGeneral, EdgeClassParent},
		{ScopeKindGeneral, EdgeClassMember},
		{ScopeKindGeneral, EdgeClassRoute},
		{ScopeKindWorkspace, EdgeClassMember},
		{ScopeKindProduct, EdgeClassMember},
		{ScopeKindGlobal, EdgeClassMember},
		{ScopeKindGlobal, EdgeClassRoute},
		{ScopeKind("bogus"), EdgeClassParent},
		{ScopeKindSession, EdgeClass("bogus")},
	}
	for _, c := range cases {
		if _, ok := Traversal(c.kind, c.edge); ok {
			t.Errorf("Traversal(%q, %q) ok = true, want false (unlisted pair must deny)", c.kind, c.edge)
		}
	}
}

func TestEdgeClassForMapping(t *testing.T) {
	cases := []struct {
		kind EdgeKind
		want EdgeClass
	}{
		{EdgeKindMemberOf, EdgeClassMember},
		{EdgeKindDependsOn, EdgeClassRoute},
		{EdgeKindSharesContextWith, EdgeClassRoute},
	}
	for _, c := range cases {
		got, ok := EdgeClassFor(c.kind)
		if !ok || got != c.want {
			t.Errorf("EdgeClassFor(%q) = %q, %v, want %q, true", c.kind, got, ok, c.want)
		}
	}
	if _, ok := EdgeClassFor(EdgeKind("bogus")); ok {
		t.Error("EdgeClassFor(\"bogus\") ok = true, want false")
	}
}

func TestEdgeClassValid(t *testing.T) {
	for _, c := range []EdgeClass{EdgeClassParent, EdgeClassMember, EdgeClassRoute} {
		if !c.Valid() {
			t.Errorf("EdgeClass(%q).Valid() = false, want true", c)
		}
	}
	if EdgeClass("bogus").Valid() {
		t.Error("EdgeClass(\"bogus\").Valid() = true, want false")
	}
}
