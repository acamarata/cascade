// Purpose: unit tests for daemonless.go: socket probe, §D-3 write
//   arbitration (real flock, real sqlite.Open, no fake lock manager), the
//   read-only fallback, and the embedded-append -> daemon-replay
//   direction.
// Constraints: package runtime_test (external): internal/events imports
//   internal/runtime, so a white-box test also importing internal/events
//   would cycle. Everything used here is already exported.
// SPORT: internal/runtime EmbeddedRuntime/ADDED (tests).
package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

type fakeConn struct{}

func (fakeConn) Close() error { return nil }

func dialerStub(live bool, err error) runtime.Dialer {
	return func(string, string, time.Duration) (io.Closer, error) {
		if live {
			return fakeConn{}, nil
		}
		return nil, err
	}
}

// TestProbeDaemonless covers absent/live/refused/undecidable, and the nil
// dial -> default dialer fallback (no case here ever needs to actually
// dial, so this proves the classification logic, not the network layer).
func TestProbeDaemonless(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if err := os.WriteFile(sockPath, nil, 0o644); err != nil {
		t.Fatalf("seed socket file: %v", err)
	}
	cases := []struct {
		name string
		path string
		dial runtime.Dialer
		want bool
	}{
		{"absent socket", filepath.Join(dir, "no.sock"), dialerStub(true, nil), true},
		{"live socket", sockPath, dialerStub(true, nil), false},
		{"refused (confirmed stale)", sockPath, dialerStub(false, syscall.ECONNREFUSED), true},
		{"undecidable falls back to embedded", sockPath, dialerStub(false, os.ErrPermission), true},
		{"nil dial uses default", filepath.Join(dir, "no2.sock"), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := runtime.ProbeDaemonless(tc.path, time.Second, tc.dial)
			if st.Embedded != tc.want {
				t.Fatalf("Embedded = %v, want %v", st.Embedded, tc.want)
			}
		})
	}
	undecidable := runtime.ProbeDaemonless(sockPath, time.Second, dialerStub(false, os.ErrPermission))
	if undecidable.ProbeErr == nil {
		t.Fatal("ProbeErr = nil, want the undecidable dial error surfaced for the caller's notice")
	}
}

func TestDaemonlessStateContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := runtime.DaemonlessStateFrom(ctx); ok {
		t.Fatal("ok = true on a bare context, want false")
	}
	want := runtime.DaemonlessState{Embedded: true, SocketPath: "/tmp/x.sock"}
	ctx = runtime.WithDaemonlessState(ctx, want)
	got, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || got != want {
		t.Fatalf("DaemonlessStateFrom = %+v, %v; want %+v, true", got, ok, want)
	}
}

// TestWriteArbitration proves §D-3 with REAL locks: two sqlite.Driver
// opens against the SAME db path, sequentially — the second Open contends
// the exclusive flock while the first is still open (daemon-holds-lock).
func TestWriteArbitration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")

	daemonDriver, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("daemon Open: %v", err)
	}
	defer func() { _ = daemonDriver.Close() }()
	if err := daemonDriver.Put(ctx, "ns", "seed", []byte("v1")); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// WRITE verb: must refuse, never proceed (never split-brain).
	_, err = runtime.OpenEmbeddedWriteStore(ctx, dbPath, nil, nil)
	if !errors.Is(err, sqlite.ErrDaemonOwnsStore) {
		t.Fatalf("error = %v, want errors.Is(_, sqlite.ErrDaemonOwnsStore)", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("Kind = %v, %v; want KindConflict, true", kind, ok)
	}

	// READ verb: a flock conflict is never a read failure.
	roStore, closeFn, err := runtime.OpenEmbeddedReadStore(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("OpenEmbeddedReadStore under a held flock: %v, want success (read-only)", err)
	}
	defer func() { _ = closeFn() }()
	got, err := roStore.Get(ctx, "ns", "seed")
	if err != nil || string(got) != "v1" {
		t.Fatalf("read-only Get = %q, %v; want v1, nil", got, err)
	}
	if err := roStore.Put(ctx, "ns", "seed", []byte("v2")); err == nil {
		t.Fatal("read-only store Put succeeded, want refusal")
	}

	// Db intact: daemon's own connection still reads v1.
	stillV1, err := daemonDriver.Get(ctx, "ns", "seed")
	if err != nil || string(stillV1) != "v1" {
		t.Fatalf("daemon-side read after arbitration attempts: %q, %v; want v1, nil", stillV1, err)
	}
}

// TestWriteArbitration_NoDaemon proves the common case: nobody holds the
// lock, so both WRITE and READ verbs get the ordinary read/write Driver;
// also exercises the injected socket-probe/migrator options.
func TestWriteArbitration_NoDaemon(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	migrated := false

	d, err := runtime.OpenEmbeddedWriteStore(ctx, dbPath,
		func(string) (bool, error) { return false, nil },
		func(context.Context, *sql.DB) error { migrated = true; return nil })
	if err != nil {
		t.Fatalf("OpenEmbeddedWriteStore with no daemon: %v", err)
	}
	if !migrated {
		t.Fatal("injected migrator was never called")
	}
	if err := d.Put(ctx, "ns", "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_ = d.Close()

	store, closeFn, err := runtime.OpenEmbeddedReadStore(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("OpenEmbeddedReadStore with no daemon: %v", err)
	}
	defer func() { _ = closeFn() }()
	v, err := store.Get(ctx, "ns", "k")
	if err != nil || string(v) != "v" {
		t.Fatalf("Get = %q, %v; want v, nil", v, err)
	}
}

// TestEmbeddedAppendReplay proves the §D-3 replay DIRECTION end to end:
// append events embedded (no daemon), close, open a SECOND independent
// Driver+Bus at the same path (standing in for "the daemon starts") and
// assert it observes the embedded-appended events via Replay.
func TestEmbeddedAppendReplay(t *testing.T) {
	ctx := context.Background()
	clock := runtime.SystemClock{}
	dbPath := filepath.Join(t.TempDir(), "cascade.db")

	embeddedDriver, err := runtime.OpenEmbeddedWriteStore(ctx, dbPath, nil, nil)
	if err != nil {
		t.Fatalf("embedded Open: %v", err)
	}
	bus := events.New(embeddedDriver, clock)
	published, err := bus.Publish(ctx, "runtime", "daemonless.append", "embedded", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("embedded Publish: %v", err)
	}
	if err := embeddedDriver.Close(); err != nil {
		t.Fatalf("embedded Close: %v", err)
	}

	daemonDriver, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("daemon Open: %v", err)
	}
	defer func() { _ = daemonDriver.Close() }()
	replayed, err := events.New(daemonDriver, clock).Replay(ctx, "runtime", 0)
	if err != nil {
		t.Fatalf("daemon Replay: %v", err)
	}
	if len(replayed) != 1 || replayed[0].Seq != published.Seq || replayed[0].Kind != published.Kind {
		t.Fatalf("daemon observed %+v, want exactly the embedded-published event %+v", replayed, published)
	}
}

// TestEmbeddedAppendReplay_EmptyStart: missing cursor/log -> safe empty
// start, not a crash.
func TestEmbeddedAppendReplay_EmptyStart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	d, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	got, err := events.New(d, runtime.SystemClock{}).Replay(ctx, "runtime", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("Replay on empty namespace = %v, %v; want 0 events, nil", got, err)
	}
}

// TestReadOnlyStore_Scan exercises the read-only fallback's Scan (prefix
// boundary + empty prefix) and Delete/Tx refusals.
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
