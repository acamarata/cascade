// Purpose: prove OpenReadOnlyStore is read-only by construction for the
//
//	doctor's audit-chain check: it reads a store a daemon holds, it creates
//	nothing for a missing database, it leaves a cleanly closed WAL database
//	byte-identical, and it refuses every write.
//
// Constraints: external test package like its siblings; no timing, no
//
//	platform-specific mode bits, no path separators in assertions.
//
// SPORT: internal/runtime OpenReadOnlyStore/ADDED (P1-SEC-12 tests).
package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// dirDigest lists every file under dir with its size and content hash, so a
// created, resized or rewritten file changes the string.
func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("%s %d %s", filepath.ToSlash(rel), len(data), hex.EncodeToString(sum[:])))
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", dir, err)
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}

func seedStore(t *testing.T, path string) *sqlite.Driver {
	t.Helper()
	seed, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if err := seed.Put(context.Background(), "ns", "alpha", []byte("one")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	return seed
}

func TestOpenReadOnlyStoreMissingDatabaseCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	before := dirDigest(t, dir)
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), filepath.Join(dir, "cascade.db"))
	if err == nil {
		_ = closeFn()
		t.Fatalf("opened a missing database (%v), want an error", store)
	}
	if store != nil || closeFn != nil {
		t.Fatalf("an error must return a nil store and close func, got %v %v", store, closeFn != nil)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || dirDigest(t, dir) != before {
		t.Fatalf("a failed read-only open created files: %v", entries)
	}
}

func TestOpenReadOnlyStoreReadsWhileWriterHoldsLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	seed := seedStore(t, dbPath)
	defer func() { _ = seed.Close() }()
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnlyStore: %v", err)
	}
	defer func() { _ = closeFn() }()
	got, err := store.Get(context.Background(), "ns", "alpha")
	if err != nil || string(got) != "one" {
		t.Fatalf("Get = %q, %v; want one", got, err)
	}
	if keys := scanKeys(t, store, "ns", ""); len(keys) != 1 || keys[0] != "alpha" {
		t.Fatalf("Scan = %v, want [alpha]", keys)
	}
}

func TestOpenReadOnlyStoreRefusesEveryWrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	seed := seedStore(t, dbPath)
	defer func() { _ = seed.Close() }()
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnlyStore: %v", err)
	}
	defer func() { _ = closeFn() }()
	ctx := context.Background()
	for name, werr := range map[string]error{
		"Put":    store.Put(ctx, "ns", "beta", []byte("two")),
		"Delete": store.Delete(ctx, "ns", "alpha"),
		"Tx":     store.Tx(ctx, func(context.Context, provider.Tx) error { return nil }),
	} {
		if werr == nil {
			t.Errorf("%s succeeded on a read-only store", name)
		}
	}
	if got, gerr := store.Get(ctx, "ns", "alpha"); gerr != nil || string(got) != "one" {
		t.Fatalf("alpha after refused writes = %q, %v; want one", got, gerr)
	}
	_, gerr := store.Get(ctx, "ns", "beta")
	if kind, ok := cascade.KindOf(gerr); !ok || kind != cascade.KindNotFound {
		t.Fatalf("beta must not exist after a refused Put, got %v", gerr)
	}
}

// TestOpenReadOnlyStoreLeavesCleanDatabaseUntouched is the escalate_if probe
// for WAL: a database closed cleanly by its writer has no -wal or -shm file,
// and a read-only open and close must not leave one behind.
func TestOpenReadOnlyStoreLeavesCleanDatabaseUntouched(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	seed := seedStore(t, dbPath)
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
	before := dirDigest(t, dir)
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnlyStore: %v", err)
	}
	if got, gerr := store.Get(context.Background(), "ns", "alpha"); gerr != nil || string(got) != "one" {
		t.Fatalf("Get = %q, %v; want one", got, gerr)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatalf("a read-only open changed the data directory\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestOpenReadOnlyStoreRefusesHalfPresentWAL: a -wal without its -shm (or the
// reverse) is a crashed writer whose recovery would write, so the open refuses
// and leaves the directory exactly as it was.
func TestOpenReadOnlyStoreRefusesHalfPresentWAL(t *testing.T) {
	for _, sidecar := range []string{"-wal", "-shm"} {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "cascade.db")
		seed := seedStore(t, dbPath)
		if err := seed.Close(); err != nil {
			t.Fatalf("seed Close: %v", err)
		}
		if err := os.WriteFile(dbPath+sidecar, []byte("leftover"), 0o600); err != nil {
			t.Fatalf("write %s: %v", sidecar, err)
		}
		before := dirDigest(t, dir)
		store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
		if err == nil {
			_ = closeFn()
			t.Fatalf("%s alone: opened %v, want a refusal", sidecar, store)
		}
		if !runtime.IsStoreInTransition(err) {
			t.Fatalf("%s alone: %v, want the sidecars-in-transition refusal", sidecar, err)
		}
		if after := dirDigest(t, dir); after != before {
			t.Fatalf("%s alone: the refused open changed the directory", sidecar)
		}
	}
}

func TestIsStoreInTransitionIgnoresOtherConflicts(t *testing.T) {
	if runtime.IsStoreInTransition(cascade.New(cascade.KindConflict, "unrelated")) || runtime.IsStoreInTransition(nil) {
		t.Fatal("an unrelated conflict must not read as a WAL transition")
	}
}

// fileSum is the content hash of one file, or "absent".
func fileSum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestOpenReadOnlyStoreReadsWhileWriterHoldsOpenTransaction: a real second
// connection holds a write transaction open (a live daemon mid-write) while
// the read-only open reads the committed snapshot; the main file and the
// -wal are unchanged by the read (the -shm is the writer's shared memory).
func TestOpenReadOnlyStoreReadsWhileWriterHoldsOpenTransaction(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	seed := seedStore(t, dbPath)
	defer func() { _ = seed.Close() }()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- seed.Tx(ctx, func(c context.Context, tx provider.Tx) error {
			if err := tx.Put(c, "ns", "pending", []byte("uncommitted")); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	beforeDB, beforeWAL := fileSum(t, dbPath), fileSum(t, dbPath+"-wal")
	store, closeFn, err := runtime.OpenReadOnlyStore(ctx, dbPath)
	if err != nil {
		close(release)
		<-done
		t.Fatalf("OpenReadOnlyStore beside an open write transaction: %v", err)
	}
	got, gerr := store.Get(ctx, "ns", "alpha")
	_, perr := store.Get(ctx, "ns", "pending")
	if cerr := closeFn(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	afterDB, afterWAL := fileSum(t, dbPath), fileSum(t, dbPath+"-wal")
	close(release)
	if terr := <-done; terr != nil {
		t.Fatalf("writer transaction: %v", terr)
	}
	if gerr != nil || string(got) != "one" {
		t.Fatalf("committed row = %q, %v; want one", got, gerr)
	}
	if kind, ok := cascade.KindOf(perr); !ok || kind != cascade.KindNotFound {
		t.Fatalf("an uncommitted row must be invisible, got %v", perr)
	}
	if beforeDB != afterDB || beforeWAL != afterWAL {
		t.Fatal("the read-only open changed cascade.db or its -wal")
	}
}

// TestOpenReadOnlyStoreRefusesStoreWithNoSchemaYet: a database file with no kv
// table is a writer mid-creation (or an empty file), a transition and not
// tampering, and the refused open creates nothing.
func TestOpenReadOnlyStoreRefusesStoreWithNoSchemaYet(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("write an empty database file: %v", err)
	}
	before := dirDigest(t, dir)
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if err == nil {
		_ = closeFn()
		t.Fatalf("opened a store with no schema (%v), want a refusal", store)
	}
	if !runtime.IsStoreInTransition(err) {
		t.Fatalf("no schema: %v, want the in-transition refusal", err)
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatal("the refused open changed the directory")
	}
}
