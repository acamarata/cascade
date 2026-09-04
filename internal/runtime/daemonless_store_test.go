// Purpose: the second half of daemonless.go's unit tests, split from
//
//	daemonless_test.go purely to stay under the file-size cap. Covers the
//	read-only store fallback, the typed refusal's nil-cause and
//	platform-reason paths, and the elevation precondition seam.
//
// Constraints: same external test package as its sibling, for the same
//
//	import-cycle reason recorded there.
//
// SPORT: internal/runtime EmbeddedRuntime/ADDED (tests).
package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

func TestReadOnlyStore_Scan(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	seed, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	for _, kv := range [][2]string{{"alpha", "1"}, {"alpha2", "2"}, {"beta", "3"}} {
		if err := seed.Put(ctx, "ns", kv[0], []byte(kv[1])); err != nil {
			t.Fatalf("seed Put %s: %v", kv[0], err)
		}
	}

	roStore, closeFn, err := runtime.OpenEmbeddedReadStore(ctx, dbPath, nil) // holds daemon flock -> fallback
	if err != nil {
		t.Fatalf("OpenEmbeddedReadStore: %v", err)
	}
	defer func() { _ = closeFn() }()
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	keys := scanKeys(t, roStore, "ns", "alpha")
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "alpha2" {
		t.Fatalf("Scan(prefix=alpha) = %v, want [alpha alpha2]", keys)
	}
	if all := scanKeys(t, roStore, "ns", ""); len(all) != 3 {
		t.Fatalf("Scan(empty prefix) = %v, want 3 keys", all)
	}
	if err := roStore.Delete(ctx, "ns", "alpha"); err == nil {
		t.Fatal("read-only Delete succeeded, want refusal")
	}
	if err := roStore.Tx(ctx, func(context.Context, provider.Tx) error { return nil }); err == nil {
		t.Fatal("read-only Tx succeeded, want refusal")
	}
	if _, err := roStore.Get(ctx, "ns", "absent"); err == nil {
		t.Fatal("Get on a missing key succeeded, want KindNotFound")
	}
}

func scanKeys(t *testing.T, s provider.Store, ns, prefix string) []string {
	t.Helper()
	it, err := s.Scan(context.Background(), ns, prefix)
	if err != nil {
		t.Fatalf("Scan(%q,%q): %v", ns, prefix, err)
	}
	defer func() { _ = it.Close() }()
	var keys []string
	for it.Next(context.Background()) {
		keys = append(keys, it.Key())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator Err: %v", err)
	}
	return keys
}

func TestErrDaemonOwnsStore_NilCause_AndPlatformReason(t *testing.T) {
	if err := runtime.ErrDaemonOwnsStore(nil); !errors.Is(err, sqlite.ErrDaemonOwnsStore) {
		t.Fatalf("ErrDaemonOwnsStore(nil) = %v, want errors.Is(_, sqlite.ErrDaemonOwnsStore)", err)
	}
	if runtime.PlatformElevationUnavailableReason() == "" {
		t.Fatal("PlatformElevationUnavailableReason() = \"\", want non-empty")
	}
}

// TestDaemonlessElevationPrecondition covers the fail-closed nil default
// and a wired-in function.
func TestDaemonlessElevationPrecondition(t *testing.T) {
	if enrolled, avail := runtime.DaemonlessElevationPrecondition(nil); enrolled || avail {
		t.Fatalf("DaemonlessElevationPrecondition(nil) = %v, %v; want false, false", enrolled, avail)
	}
	if enrolled, avail := runtime.DaemonlessElevationPrecondition(func() (bool, bool) { return true, true }); !enrolled || !avail {
		t.Fatalf("DaemonlessElevationPrecondition(fn) = %v, %v; want true, true", enrolled, avail)
	}
}
