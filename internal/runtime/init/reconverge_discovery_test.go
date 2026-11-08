package init

import (
	"context"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeDiscoveryStore holds the discovery state in memory and logs every
// write, so a test can count duplicates.
type fakeDiscoveryStore struct {
	have   DiscoveryState
	writes []string
}

func (s *fakeDiscoveryStore) Discovery(context.Context) (DiscoveryState, error) {
	return DiscoveryState{
		Roots:     append([]string(nil), s.have.Roots...),
		Nodes:     append([]string(nil), s.have.Nodes...),
		Providers: append([]string(nil), s.have.Providers...),
		Policy:    s.have.Policy,
	}, nil
}

func (s *fakeDiscoveryStore) AddRoot(_ context.Context, path string) error {
	s.writes = append(s.writes, "root:"+path)
	s.have.Roots = append(s.have.Roots, path)
	return nil
}

func (s *fakeDiscoveryStore) AddNode(_ context.Context, host string) error {
	s.writes = append(s.writes, "node:"+host)
	s.have.Nodes = append(s.have.Nodes, host)
	return nil
}

func (s *fakeDiscoveryStore) AddProvider(_ context.Context, name string) error {
	s.writes = append(s.writes, "provider:"+name)
	s.have.Providers = append(s.have.Providers, name)
	return nil
}

func (s *fakeDiscoveryStore) SetPolicy(_ context.Context, preset string) error {
	s.writes = append(s.writes, "policy:"+preset)
	s.have.Policy = preset
	return nil
}

// TestReconvergeDiscoveryIdempotent proves the reconverge is additive: a
// first run upserts only what is missing and keeps what exists, and an
// identical second run returns an empty delta with no write at all.
func TestReconvergeDiscoveryIdempotent(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	store := &fakeDiscoveryStore{have: DiscoveryState{
		Roots: []string{"/r/kept"}, Nodes: []string{"kept.invalid"},
		Providers: []string{"kept"}, Policy: "balanced",
	}}
	desired := DiscoveryState{
		Roots:     []string{"/r/new", "/r/kept"},
		Nodes:     []string{"new.invalid", "KEPT.invalid"},
		Providers: []string{"new", "kept"},
		Policy:    "strict",
	}
	delta, err := ReconvergeDiscovery(ctx, store, desired)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	want := DiscoveryDelta{Roots: []string{"/r/new"}, Nodes: []string{"new.invalid"},
		Providers: []string{"new"}, Policy: "strict"}
	if !reflect.DeepEqual(delta, want) {
		t.Fatalf("first delta = %+v, want %+v", delta, want)
	}
	for _, kept := range [][]string{store.have.Roots, store.have.Nodes, store.have.Providers} {
		if len(kept) != 2 {
			t.Fatalf("a replay removed or duplicated an entry: %+v", store.have)
		}
	}
	first := len(store.writes)

	delta, err = ReconvergeDiscovery(ctx, store, desired)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !delta.Empty() || !reflect.DeepEqual(delta, DiscoveryDelta{}) {
		t.Fatalf("second delta = %+v, want empty", delta)
	}
	if len(store.writes) != first {
		t.Fatalf("second run wrote %v", store.writes[first:])
	}

	_, err = ReconvergeDiscovery(ctx, store, DiscoveryState{Policy: "lax"})
	assertTaxonomyErr(t, err, cascade.KindInvalidInput,
		`cascade init: "lax" is not a policy preset; choose one of balanced, strict, custom`)
	if len(store.writes) != first {
		t.Fatalf("a refused preset wrote %v", store.writes[first:])
	}
}
