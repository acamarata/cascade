package mod

import (
	"testing"
)

func TestCoreMembersParsesProfile(t *testing.T) {
	src := []byte(`package master

var coreProfile = []string{"ci", "store"}
`)
	members, err := CoreMembers(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if !members["ci"] || !members["store"] {
		t.Fatalf("expected ci and store, got %v", members)
	}
}

func TestCoreMembersEmptyFailsClosed(t *testing.T) {
	members, err := CoreMembers([]byte(""))
	if err != ErrNoCoreProfile {
		t.Fatalf("expected ErrNoCoreProfile, got %v", err)
	}
	if members != nil {
		t.Fatalf("expected nil map, got %v", members)
	}
}

func TestIsCoreMatchesProfile(t *testing.T) {
	members := map[string]bool{"ci": true, "store": true}
	if !IsCore("ci", members) {
		t.Fatalf("expected ci to be core")
	}
	if IsCore("web", members) {
		t.Fatalf("expected web to not be core")
	}
}

func TestNoFallbackList(t *testing.T) {
	src := []byte(`package master

var coreProfile = []string{"ci"}
`)
	members, err := CoreMembers(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if IsCore("store", members) {
		t.Fatalf("expected store to not be core")
	}
}
