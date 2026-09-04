package memory

import (
	"context"
	"testing"
)

func TestListAndExists(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)

	names, err := s.List(ctx, KindUser)
	if err != nil || len(names) != 0 {
		t.Fatalf("List on an empty store = %v, %v", names, err)
	}
	for _, n := range []string{"zebra", "alpha", "middle"} {
		e := validEntry()
		e.Name = n
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", n, err)
		}
	}
	got, err := s.List(ctx, KindProject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "middle", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List = %v, want %v (lexical order is part of the contract)", got, want)
		}
	}
	if ok, err := s.Exists(ctx, KindProject, "alpha"); err != nil || !ok {
		t.Errorf("Exists(alpha) = %v, %v", ok, err)
	}
	if ok, err := s.Exists(ctx, KindProject, "absent"); err != nil || ok {
		t.Errorf("Exists(absent) = %v, %v", ok, err)
	}
}
