package plugins

// Purpose (this file): two fail-closed branches of this package that no
// existing test reached. Both are error paths whose whole job is to STOP
// rather than continue with a half-answer, which is exactly the class of
// branch an uncovered line hides:
//
//   - provisionRemoteRuntime, when it has to build the production
//     Interceptor itself and the plugin-remote egress class is disabled
//     (its default): it must surface that refusal, never call
//     remote.Dispatch with a nil Interceptor.
//   - ListMetadata, when the store's iterator stops on an error: it must
//     return that error, never the TRUNCATED record list it had already
//     accumulated -- a silently short `plugin list` is how an installed
//     plugin appears uninstalled.
//   - bridgeStateAdapter.Close on an adapter that never opened a database:
//     it must be a no-op, not a nil-pointer dereference, because the
//     drain path calls Close unconditionally on every early-exit route.
//
// It exists as its own file because internal/plugins sits on a coverage
// ratchet these branches were the honest way to clear.
//
// SPORT: internal/plugins coverage-margin tests (ADD) -- P1-E25-W5-S51-T3.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestProvisionRemoteRuntime_RefusesWhenTheInterceptorCannotBeBuilt drives
// the one branch that separates "no interceptor was injected" from
// "dispatch without one": with enableRemoteRuntime true and no injected
// Interceptor, provisionRemoteRuntime must build the production one, and
// the plugin-remote egress class being disabled by default makes that
// refuse (see dispatch_remote.go's own header).
func TestProvisionRemoteRuntime_RefusesWhenTheInterceptorCannotBeBuilt(t *testing.T) {
	m := plugin.Manifest{ID: "cascade-remote-probe"}
	m.Remote.Host = "127.0.0.1"
	m.Remote.Port = 7777

	err := provisionRemoteRuntime(context.Background(), m, true, nil)
	if err == nil {
		t.Fatal("provisionRemoteRuntime: want the interceptor-build refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
}

// erringIterator is a real iterator that reports a failure from Err after
// the loop ends -- the shape a backend read error takes.
type erringIterator struct{ provider.Iterator }

// Err implements provider.Iterator.
func (erringIterator) Err() error { return errStoreFailure }

// erringIterStore is a real MemStore whose Scan hands back that iterator.
type erringIterStore struct{ *storetest.MemStore }

// Scan implements provider.Store.
func (s erringIterStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	it, err := s.MemStore.Scan(ctx, namespace, prefix)
	if err != nil {
		return nil, err
	}
	return erringIterator{Iterator: it}, nil
}

// TestListMetadata_SurfacesAnIterationFailureRatherThanATruncatedList
// proves ListMetadata checks Iterator.Err after the loop: a store that
// yields one record and then reports a read failure must produce the
// failure, not that single record.
func TestListMetadata_SurfacesAnIterationFailureRatherThanATruncatedList(t *testing.T) {
	mem := storetest.NewMemStore()
	if err := SaveMetadata(context.Background(), mem, PluginMetadata{Name: "alpha", Enabled: true}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	recs, err := ListMetadata(context.Background(), erringIterStore{MemStore: mem})
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("err = %v, want the iterator's own failure", err)
	}
	if recs != nil {
		t.Fatalf("recs = %+v, want none: a truncated list must never be returned as the answer", recs)
	}
}

// TestBridgeStateAdapterClose_IsANoOpWithoutADatabase pins the nil-db
// guard: every early-exit route through the cascade-pa bridge calls Close
// unconditionally, including ones that never got as far as opening
// cascade.db, so this must return nil rather than panic.
func TestBridgeStateAdapterClose_IsANoOpWithoutADatabase(t *testing.T) {
	if err := (bridgeStateAdapter{}).Close(); err != nil {
		t.Fatalf("Close on an unopened adapter = %v, want nil", err)
	}
}
