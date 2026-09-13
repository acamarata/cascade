// Purpose: unit tests for resolveRecallEmbedded and openEmbeddedFTSLeg's
// own error branches (recall_embedded.go) — the two REAL, OS-level
// failure paths the embedded composition can hit that recall_virgin_home_
// test.go's end-to-end cases never provoke: os.MkdirAll refusing to
// create the retrieval index directory, and sqlite.Open refusing to open
// cascade.db. Both are forced with real filesystem collisions (a file or
// directory already occupying the path the code needs to create), never
// a mock — the same technique internal/runtime's own MkdirAll-failure
// tests use.
//
// recall_embedded.go carries three OTHER error branches this file does
// not exercise (recall.NewService's nil-catalog check, json.Marshal on
// recall.QueryParams, and the out.(recall.QueryResult) type assertion):
// all three guard conditions that recall_embedded.go's own call sites
// make structurally impossible (NewFileCatalog never returns nil,
// QueryParams holds only plain scalars/strings/slices, and
// recall.Handler.Query's success path always returns a concrete
// QueryResult) — defensive code with no reachable trigger, not
// undertested behaviour.
//
// SPORT: cmd.cascade.cmd.recall (TEST, embedded-path error branches).
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestResolveRecallEmbedded_MkdirAllFailure proves the os.MkdirAll
// refusal at recall_embedded.go's top: DataDir() itself resolves to a
// path a regular FILE already occupies, so MkdirAll(DataDir()+"/retrieval")
// cannot create the directory (ENOTDIR on every OS this runs on) and the
// scrubbed KindUnavailable refusal — never a panic or an unwrapped OS
// error — is what a caller sees.
func TestResolveRecallEmbedded_MkdirAllFailure(t *testing.T) {
	root := t.TempDir()
	dataDirAsFile := filepath.Join(root, "data")
	if err := os.WriteFile(dataDirAsFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("test setup: seed a file at the would-be data dir: %v", err)
	}
	deps := recallDeps{Paths: fakeMemoryPaths{root: root}}

	_, err := resolveRecallEmbedded(context.Background(), deps, recall.QueryParams{Query: "x", Scope: "project/cascade"})
	if err == nil {
		t.Fatal("resolveRecallEmbedded over an unwritable data dir: want an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "create retrieval index directory") {
		t.Fatalf("err = %v, want it to name the MkdirAll step", err)
	}
}

// TestResolveRecallEmbedded_OpenEmbeddedFTSLegFailure proves the second
// refusal: the retrieval index directory creates cleanly, but a
// DIRECTORY already occupies the exact path cascade.db needs
// (DataDir()/cascade.db), so openEmbeddedFTSLeg's sqlite.Open call
// refuses — proving BOTH that resolveRecallEmbedded surfaces
// openEmbeddedFTSLeg's error (its own "if err != nil" branch) and that
// openEmbeddedFTSLeg's own sqlite.Open refusal fires for real, over a
// real collision, not a stub.
func TestResolveRecallEmbedded_OpenEmbeddedFTSLegFailure(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("test setup: mkdir data dir: %v", err)
	}
	// cascade.db is a directory, not a file: sqlite.Open must refuse to
	// open it as a database rather than silently succeeding or panicking.
	if err := os.MkdirAll(filepath.Join(dataDir, "cascade.db"), 0o700); err != nil {
		t.Fatalf("test setup: seed a directory at cascade.db's path: %v", err)
	}
	deps := recallDeps{Paths: fakeMemoryPaths{root: root}}

	_, err := resolveRecallEmbedded(context.Background(), deps, recall.QueryParams{Query: "x", Scope: "project/cascade"})
	if err == nil {
		t.Fatal("resolveRecallEmbedded with cascade.db occupied by a directory: want an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}

	// The retrieval index directory itself must still have been created:
	// the MkdirAll step this test does NOT exercise ran and succeeded, so
	// the failure is provably openEmbeddedFTSLeg's, not a repeat of
	// TestResolveRecallEmbedded_MkdirAllFailure's.
	if info, statErr := os.Stat(filepath.Join(dataDir, "retrieval")); statErr != nil || !info.IsDir() {
		t.Errorf("the retrieval index directory was not created before the cascade.db failure: stat err=%v", statErr)
	}
}

// TestOpenEmbeddedFTSLeg_Success is the positive-path unit proof for
// openEmbeddedFTSLeg in isolation (recall_virgin_home_test.go only
// exercises it indirectly, through the full CLI): over a real, writable
// data dir it returns a non-nil recall.Leg and a working closer, and
// cascade.db exists afterward — sqlite.Open's own documented "creates the
// file when it does not exist yet" contract (openEmbeddedFTSLeg's doc
// comment), proven rather than assumed.
func TestOpenEmbeddedFTSLeg_Success(t *testing.T) {
	root := t.TempDir()
	paths := fakeMemoryPaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("test setup: mkdir data dir: %v", err)
	}

	leg, closeStore, err := openEmbeddedFTSLeg(context.Background(), paths)
	if err != nil {
		t.Fatalf("openEmbeddedFTSLeg: %v", err)
	}
	if leg == nil {
		t.Fatal("openEmbeddedFTSLeg returned a nil recall.Leg")
	}
	if closeStore == nil {
		t.Fatal("openEmbeddedFTSLeg returned a nil closer")
	}
	closeStore()

	if _, statErr := os.Stat(filepath.Join(paths.DataDir(), "cascade.db")); statErr != nil {
		t.Errorf("openEmbeddedFTSLeg did not create cascade.db: stat err=%v", statErr)
	}
}

// TestOpenEmbeddedFTSLeg_OpenFailure is openEmbeddedFTSLeg's own direct
// unit proof of the sqlite.Open refusal (the end-to-end proof above
// drives it only through resolveRecallEmbedded): a directory already
// occupies cascade.db's path, so sqlite.Open must refuse and the
// function must return a nil leg and a nil closer, never a leg a caller
// could use after a refused open.
func TestOpenEmbeddedFTSLeg_OpenFailure(t *testing.T) {
	root := t.TempDir()
	paths := fakeMemoryPaths{root: root}
	if err := os.MkdirAll(filepath.Join(paths.DataDir(), "cascade.db"), 0o700); err != nil {
		t.Fatalf("test setup: seed a directory at cascade.db's path: %v", err)
	}

	leg, closeStore, err := openEmbeddedFTSLeg(context.Background(), paths)
	if err == nil {
		t.Fatal("openEmbeddedFTSLeg over a cascade.db path occupied by a directory: want an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "open cascade.db") {
		t.Fatalf("err = %v, want it to name the cascade.db open step", err)
	}
	if leg != nil {
		t.Error("openEmbeddedFTSLeg returned a non-nil leg alongside an error")
	}
	if closeStore != nil {
		t.Error("openEmbeddedFTSLeg returned a non-nil closer alongside an error")
	}
}
