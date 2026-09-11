package evidence

// Purpose: FetchContent's git path against a REAL git repository created
// in t.TempDir() (Art.2): resolves at the recorded commit (never
// working-tree HEAD) and retires the claim on a moved/rewritten range.
//
// SPORT: evidence/claim-record (ADD), R-21.78.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/fs"
)

// initGitRepo creates a real git repository under t.TempDir(), commits
// path with content, and returns the repo dir and the commit sha.
func initGitRepo(t *testing.T, path, content string) (repoDir, commit string) {
	t.Helper()
	repoDir = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cascade-test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=cascade-test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repoDir, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	run("add", path)
	run("commit", "-q", "-m", "initial")
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	commit = string(out[:40])
	return repoDir, commit
}

func newFetcher(t *testing.T) (*Fetcher, *Store) {
	t.Helper()
	store, _ := newStore(t)
	return NewFetcher(store, nil), store
}

func TestExpandResolvesAtRecordedCommit(t *testing.T) {
	repoDir, commit := initGitRepo(t, "file.txt", "line one\nline two\nline three\n")
	f, store := newFetcher(t)
	ctx := context.Background()

	mustPutClaimWithEvidence(t, store, "CLM-GIT")
	full := "line one\nline two" // lines 1-2, joined without a trailing newline (sliceContent's join semantics)
	e := Evidence{
		ID: "EVD-GIT", ClaimID: "CLM-GIT",
		Source: Source{Type: SourceGit, Repository: repoDir, Commit: commit, Path: "file.txt",
			Locator: Locator{Kind: LocatorLines, Start: 1, End: 2}},
		ContentHash: ContentHash([]byte(full)), DataClass: DataClassInternal,
	}
	if err := store.PutEvidence(ctx, e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}

	// Edit the working tree past the commit WITHOUT committing -- FetchContent
	// must still read the recorded commit's blob, never working-tree HEAD.
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("TAMPERED\n"), 0o644); err != nil {
		t.Fatalf("tamper working tree: %v", err)
	}

	item, err := f.FetchContent(ctx, e)
	if err != nil {
		t.Fatalf("FetchContent: %v", err)
	}
	if string(item.Bytes) != full {
		t.Errorf("FetchContent bytes = %q, want %q (working-tree HEAD leaked through)", item.Bytes, full)
	}
}

func TestExpandStaleEvidence(t *testing.T) {
	repoDir, commit := initGitRepo(t, "file.txt", "alpha\nbeta\ngamma\n")
	f, store := newFetcher(t)
	ctx := context.Background()

	mustPutClaimWithEvidence(t, store, "CLM-STALE")
	e := Evidence{
		ID: "EVD-STALE", ClaimID: "CLM-STALE",
		Source: Source{Type: SourceGit, Repository: repoDir, Commit: commit, Path: "file.txt",
			Locator: Locator{Kind: LocatorLines, Start: 1, End: 1}},
		// A content_hash that does NOT match "alpha" -- simulating a range
		// whose recorded bytes no longer match what the commit actually
		// holds (e.g. the row was written against a different capture).
		ContentHash: ContentHash([]byte("not-alpha")), DataClass: DataClassInternal,
	}
	if err := store.PutEvidence(ctx, e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}

	_, err := f.FetchContent(ctx, e)
	if err != ErrEvidenceStale {
		t.Fatalf("FetchContent(stale) = %v, want ErrEvidenceStale", err)
	}
	claim, getErr := store.GetClaim(ctx, "CLM-STALE")
	if getErr != nil {
		t.Fatalf("GetClaim: %v", getErr)
	}
	if claim.InvalidatedAt == nil {
		t.Error("stale fetch did not set claim.InvalidatedAt")
	}
}

func TestFetchContentCapturedContentHashMismatch(t *testing.T) {
	f, store := newFetcher(t)
	ctx := context.Background()
	mustPutClaimWithEvidence(t, store, "CLM-CAP")
	e := Evidence{
		ID: "EVD-CAP", ClaimID: "CLM-CAP",
		Source: Source{Type: SourceArtifact, Locator: Locator{Kind: LocatorArtifact, ArtifactRef: "ART-x"}},
		// CapturedRef does not parse to a valid provider.Hash, which
		// exercises the malformed-ref path of fetchCaptured/parseCapturedRef.
		CapturedRef: "artifact://not-a-real-artifact-id",
		ContentHash: "whatever", DataClass: DataClassInternal,
	}
	if err := store.PutEvidence(ctx, e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	if _, err := f.FetchContent(ctx, e); err == nil {
		t.Fatal("FetchContent with a malformed captured_ref = nil error, want a typed error")
	}
}

func TestFetchContentCapturedSuccess(t *testing.T) {
	store, _ := newStore(t)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	f := NewFetcher(store, blobs)
	ctx := context.Background()

	data := []byte("captured bytes")
	hash, err := blobs.Put(ctx, "evidence", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("blobs.Put: %v", err)
	}
	capturedRef := artifactScheme + artifactIDPrefix + hash.String()

	mustPutClaimWithEvidence(t, store, "CLM-FILE")
	e := Evidence{
		ID: "EVD-FILE", ClaimID: "CLM-FILE",
		Source:      Source{Type: SourceFile, Path: "f.txt", Locator: Locator{Kind: LocatorArtifact, ArtifactRef: capturedRef}},
		ContentHash: ContentHash(data), DataClass: DataClassInternal, CapturedRef: capturedRef,
	}
	if err := store.PutEvidence(ctx, e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	item, err := f.FetchContent(ctx, e)
	if err != nil {
		t.Fatalf("FetchContent: %v", err)
	}
	if string(item.Bytes) != string(data) {
		t.Errorf("FetchContent bytes = %q, want %q", item.Bytes, data)
	}
}

func TestSliceContentArtifactAndInvalidKind(t *testing.T) {
	data := []byte("whole range")
	got, err := sliceContent(data, Locator{Kind: LocatorArtifact, ArtifactRef: "x"})
	if err != nil || string(got) != string(data) {
		t.Errorf("sliceContent(artifact) = (%q, %v), want (%q, nil)", got, err, data)
	}
	if _, err := sliceContent(data, Locator{Kind: "bogus"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("sliceContent(bogus kind) = %v, want typed invalid-input", err)
	}
}
