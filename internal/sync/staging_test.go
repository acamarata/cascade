package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestStagingHappyPathAdmitsBlob(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("the real blob content")
	addr := ContentAddress(blake3.Sum256(payload))

	if err := BeginStaging(dir, addr, 1); err != nil {
		t.Fatalf("BeginStaging: %v", err)
	}
	if err := AppendStagedChunk(dir, addr, 0, 1, payload); err != nil {
		t.Fatalf("AppendStagedChunk: %v", err)
	}
	dest := filepath.Join(dir, "blobs", addr.Hex())
	if err := AdmitBlob(dir, addr, dest); err != nil {
		t.Fatalf("AdmitBlob: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("admitted blob content mismatch: %v, %q", err, got)
	}
}

func TestBlobStagingDigestMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	declared := ContentAddress(blake3.Sum256([]byte("expected content")))
	if err := BeginStaging(dir, declared, 1); err != nil {
		t.Fatalf("BeginStaging: %v", err)
	}
	// Stage the WRONG bytes under the declared address.
	if err := AppendStagedChunk(dir, declared, 0, 1, []byte("tampered content")); err != nil {
		t.Fatalf("AppendStagedChunk: %v", err)
	}
	dest := filepath.Join(dir, "blobs", declared.Hex())
	err := AdmitBlob(dir, declared, dest)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("digest mismatch: want KindIntegrity, got %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a digest mismatch must never create the destination path")
	}
}

func TestInterruptedBlobTransferNotAdmitted(t *testing.T) {
	dir := t.TempDir()
	full := []byte("a blob sent in two chunks across a dropped connection")
	addr := ContentAddress(blake3.Sum256(full))
	if err := BeginStaging(dir, addr, 2); err != nil {
		t.Fatalf("BeginStaging: %v", err)
	}
	// Only the FIRST chunk lands before the "connection drops" — the
	// second chunk never arrives, simulating a mid-transfer interruption.
	if err := AppendStagedChunk(dir, addr, 0, 2, full[:10]); err != nil {
		t.Fatalf("AppendStagedChunk: %v", err)
	}
	dest := filepath.Join(dir, "blobs", addr.Hex())
	err := AdmitBlob(dir, addr, dest)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("interrupted transfer admission: want KindIntegrity, got %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("an interrupted transfer must never produce a destination file")
	}
	// Only the .tmp path (now discarded by AdmitBlob's failure path) or
	// nothing exists at dest — the visible-as-complete file never appears.
	tmpPath, _ := stagingPaths(dir, addr)
	if _, statErr := os.Stat(tmpPath); statErr == nil {
		t.Fatal("AdmitBlob must discard the staged .tmp file on a digest mismatch")
	}
}

func TestResumeStagingReportsLastAckedSeq(t *testing.T) {
	dir := t.TempDir()
	full := []byte("resumable blob content across two chunks!")
	addr := ContentAddress(blake3.Sum256(full))
	if err := BeginStaging(dir, addr, 2); err != nil {
		t.Fatalf("BeginStaging: %v", err)
	}
	if err := AppendStagedChunk(dir, addr, 0, 2, full[:20]); err != nil {
		t.Fatalf("AppendStagedChunk: %v", err)
	}
	// Simulate a fresh process instance resuming: no in-memory state,
	// only the staging dir on disk.
	seq, err := ResumeStaging(dir, addr)
	if err != nil {
		t.Fatalf("ResumeStaging: %v", err)
	}
	if seq != 1 {
		t.Fatalf("ResumeStaging seq = %d, want 1 (resume from chunk index 1)", seq)
	}
	if err := AppendStagedChunk(dir, addr, 1, 2, full[20:]); err != nil {
		t.Fatalf("AppendStagedChunk: %v", err)
	}
	dest := filepath.Join(dir, "blobs", addr.Hex())
	if err := AdmitBlob(dir, addr, dest); err != nil {
		t.Fatalf("AdmitBlob after resume: %v", err)
	}
}

func TestResumeStagingRefusesWithoutCursor(t *testing.T) {
	dir := t.TempDir()
	addr := ContentAddress(blake3.Sum256([]byte("orphaned tmp file")))
	tmpPath, _ := stagingPaths(dir, addr)
	if err := os.WriteFile(tmpPath, []byte("some bytes with no cursor sidecar"), 0o600); err != nil {
		t.Fatalf("write orphan tmp: %v", err)
	}
	if _, err := ResumeStaging(dir, addr); err == nil {
		t.Fatal("resuming a .tmp file with no cursor sidecar must be refused, never silently trusted")
	}
}

func TestAppendStagedChunkWithoutBeginStagingFails(t *testing.T) {
	dir := t.TempDir()
	addr := ContentAddress(blake3.Sum256([]byte("never begun")))
	if err := AppendStagedChunk(dir, addr, 0, 1, []byte("x")); err == nil {
		t.Fatal("appending to a stream that was never BeginStaging'd must fail")
	}
}

func TestAdmitBlobWithoutAnyStagedFileFails(t *testing.T) {
	dir := t.TempDir()
	addr := ContentAddress(blake3.Sum256([]byte("never staged")))
	if err := AdmitBlob(dir, addr, filepath.Join(dir, "blobs", addr.Hex())); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("admit with nothing staged: want KindUnavailable, got %v", err)
	}
}

func TestBeginStagingRejectsUnwritableDir(t *testing.T) {
	// This test denies write access to a directory and asserts BeginStaging
	// refuses to create its staging subdirectory under it. root ignores
	// permission bits entirely, so the denial never takes effect and the
	// assertion would fail for a reason unrelated to the code under test.
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict root, so this case cannot be exercised")
	}
	// makeDirGenuinelyUnwritable (dirunwritable_unix_test.go /
	// dirunwritable_windows_test.go) applies a real, platform-appropriate
	// write denial: POSIX chmod on unix, an explicit DACL DENY ACE on
	// Windows, where os.Chmod only toggles a cosmetic
	// FILE_ATTRIBUTE_READONLY that never blocks writes into a directory.
	// The Windows helper proves the denial actually took effect by
	// attempting a real write before returning, so this test cannot pass
	// for the wrong reason.
	locked := t.TempDir()
	makeDirGenuinelyUnwritable(t, locked)
	addr := ContentAddress(blake3.Sum256([]byte("x")))
	dir := filepath.Join(locked, "staging")
	if err := BeginStaging(dir, addr, 1); err == nil {
		t.Fatal("BeginStaging under a genuinely unwritable parent must fail")
	}
}

func TestResumeStagingRefusesWithoutAnyStagedFile(t *testing.T) {
	dir := t.TempDir()
	addr := ContentAddress(blake3.Sum256([]byte("never staged")))
	if _, err := ResumeStaging(dir, addr); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("resume with nothing staged: want KindNotFound, got %v", err)
	}
}
