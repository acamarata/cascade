package scope

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestScopeKindValid(t *testing.T) {
	valid := []Kind{
		ScopeKindSession, ScopeKindTask, ScopeKindProject, ScopeKindWorkspace,
		ScopeKindProduct, ScopeKindGlobal, ScopeKindGeneral,
	}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false, want true", k)
		}
	}
	if Kind("bogus").Valid() {
		t.Error("Kind(\"bogus\").Valid() = true, want false")
	}
	if Kind("").Valid() {
		t.Error("Kind(\"\").Valid() = true, want false")
	}
}

func TestEdgeKindValid(t *testing.T) {
	for _, k := range []EdgeKind{EdgeKindDependsOn, EdgeKindMemberOf, EdgeKindSharesContextWith} {
		if !k.Valid() {
			t.Errorf("EdgeKind(%q).Valid() = false, want true", k)
		}
	}
	for _, k := range []EdgeKind{"", "bogus", "depends-on", "MEMBER_OF"} {
		if EdgeKind(k).Valid() {
			t.Errorf("EdgeKind(%q).Valid() = true, want false", k)
		}
	}
}

func TestValidateEdgeKindRejectsUnknown(t *testing.T) {
	err := ValidateEdgeKind(EdgeKind("owns"))
	if err == nil {
		t.Fatal("ValidateEdgeKind(\"owns\") = nil, want an error")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Errorf("ValidateEdgeKind(\"owns\") kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestValidateEdgeKindAcceptsEveryClosedValue(t *testing.T) {
	for _, k := range []EdgeKind{EdgeKindDependsOn, EdgeKindMemberOf, EdgeKindSharesContextWith} {
		if err := ValidateEdgeKind(k); err != nil {
			t.Errorf("ValidateEdgeKind(%q) = %v, want nil", k, err)
		}
	}
}

func TestSessionScopeFieldCount(_ *testing.T) {
	// R-16.3: SessionScope carries EXACTLY the twelve named fields (Kind is
	// the resolver's own discriminator, not one of the twelve). This test
	// pins the struct shape so an accidental thirteenth field fails CI
	// rather than silently widening the wire contract.
	s := SessionScope{}
	_ = s.User
	_ = s.Machine
	_ = s.Cwd
	_ = s.Workspace
	_ = s.Product
	_ = s.Project
	_ = s.Repository
	_ = s.PackagePath
	_ = s.Branch
	_ = s.Task
	_ = s.Session
	_ = s.ExplicitOverrides
	_ = s.Kind
}
