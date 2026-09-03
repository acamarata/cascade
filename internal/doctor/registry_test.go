package doctor

import (
	"testing"
)

func TestCheckRegistry_RegisterLookupList(t *testing.T) {
	reg := NewCheckRegistry()
	a := &fakeCheck{name: "a", meta: CheckMeta{FirstRun: true}}
	b := &fakeCheck{name: "b", meta: CheckMeta{FirstRun: false}}
	reg.Register(a)
	reg.Register(b)

	if reg.Len() != 2 {
		t.Fatalf("got Len()=%d, want 2", reg.Len())
	}
	got, ok := reg.Lookup("a")
	if !ok || got.Name() != "a" {
		t.Fatalf("Lookup(a) = %v, %v", got, ok)
	}
	if _, ok := reg.Lookup("missing"); ok {
		t.Fatalf("Lookup(missing) unexpectedly found")
	}

	list := reg.List()
	if len(list) != 2 || list[0].Name() != "a" || list[1].Name() != "b" {
		t.Fatalf("List() not sorted by name: %+v", names(list))
	}
}

func TestCheckRegistry_FirstRunFilters(t *testing.T) {
	reg := NewCheckRegistry()
	reg.Register(&fakeCheck{name: "fr", meta: CheckMeta{FirstRun: true}})
	reg.Register(&fakeCheck{name: "not-fr", meta: CheckMeta{FirstRun: false}})

	fr := reg.FirstRun()
	if len(fr) != 1 || fr[0].Name() != "fr" {
		t.Fatalf("FirstRun() = %+v, want only [fr]", names(fr))
	}
}

func TestCheckRegistry_DuplicateNamePanics(t *testing.T) {
	reg := NewCheckRegistry()
	reg.Register(&fakeCheck{name: "dup"})
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on duplicate registration")
		}
	}()
	reg.Register(&fakeCheck{name: "dup"})
}

func TestCheckRegistry_EmptyNamePanics(t *testing.T) {
	reg := NewCheckRegistry()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on empty Name()")
		}
	}()
	reg.Register(&fakeCheck{name: ""})
}

func names(checks []Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name()
	}
	return out
}

// compile-time assertion that fakeCheck satisfies Check.
var _ Check = (*fakeCheck)(nil)
