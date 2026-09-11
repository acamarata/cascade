package evidence

// Purpose: FetchContent resolves one Evidence row's Locator to bytes,
// verified against ContentHash before any byte is returned (R-21.78).
//
// F/S-10.T4 NOTE (quoted in the journal): the contract names "the F/S-10.T4
// fetchers" as the bounded-fetch mechanism this ticket builds on. No
// internal/retrieval fetcher package exists anywhere in the tree (a
// repo-wide search for a Fetcher type or a Fetch function turns up
// nothing), so this file reads a git source directly via `git show
// <commit>:<path>` (the same exec.Command("git", ...) pattern
// internal/build/commits.go and internal/inventory/tree.go already use in
// this repo) rather than depending on a package that does not exist yet.
// Source.Repository is treated as a filesystem path to the git repository
// -- there is no repo-id registry in the tree to resolve a symbolic
// repository id against, so this is the simplest composition that
// satisfies the contract's own test requirement ("a REAL git repository
// created in t.TempDir() by the test").
//
// Inputs: an Evidence row.
// Outputs: a ContentItem, or a typed error -- ErrEvidenceStale (no
// content, claim invalidated) on a git hash mismatch, ErrContentHashMismatch
// on a captured-content hash mismatch.
// Constraints: a git source is read AT THE RECORDED COMMIT, never at
// working-tree HEAD; non-git sources are served from the bytes captured
// at capture time and named by CapturedRef, never re-read from their
// origin. Every item's Trust resolves to corpus.TrustUntrustedSource: no
// fetcher in this tree ever attaches a corpus's own TrustTrusted tag to an
// evidence Source, so the fail-closed default is the only path this
// ticket can honestly exercise (06 SS5.16).
//
// SPORT: evidence/claim-record (ADD), R-21.78.

import (
	"context"
	"encoding/hex"
	"io"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ContentItem is one page's worth of resolved evidence bytes, the R-21.41
// expand-packet element.
type ContentItem struct {
	EvidenceID string            `json:"evidence_id"`
	Bytes      []byte            `json:"bytes"`
	Truncated  bool              `json:"truncated"`
	Trust      corpus.TrustLevel `json:"trust"`
}

// Fetcher resolves Evidence rows to verified bytes and pages them for
// Expand.
type Fetcher struct {
	store *Store
	blobs provider.BlobStore
}

// NewFetcher returns a Fetcher over store (for Invalidate on a stale git
// read) and blobs (the content-addressed store non-git sources are served
// from).
func NewFetcher(store *Store, blobs provider.BlobStore) *Fetcher {
	return &Fetcher{store: store, blobs: blobs}
}

// errOutOfRange marks a locator that no longer fits the fetched content --
// fetchGit translates this into ErrEvidenceStale; it never escapes this
// file.
var errOutOfRange = cascade.New(cascade.KindIntegrity, "evidence: locator out of range")

// FetchContent resolves e's Locator to bytes, verified against
// e.ContentHash before any byte is returned.
func (f *Fetcher) FetchContent(ctx context.Context, e Evidence) (ContentItem, error) {
	var full []byte
	var err error
	if e.Source.Type == SourceGit {
		full, err = f.fetchGit(ctx, e)
		if err != nil {
			return ContentItem{}, err
		}
	} else {
		full, err = f.fetchCaptured(ctx, e)
		if err != nil {
			return ContentItem{}, err
		}
		if ContentHash(full) != e.ContentHash {
			return ContentItem{}, ErrContentHashMismatch
		}
	}
	return ContentItem{EvidenceID: e.ID, Bytes: full, Trust: corpus.TrustUntrustedSource}, nil
}

func (f *Fetcher) fetchGit(ctx context.Context, e Evidence) ([]byte, error) {
	blob, err := gitShow(ctx, e.Source.Repository, e.Source.Commit, e.Source.Path)
	if err != nil {
		return nil, err
	}
	sliced, err := sliceContent(blob, e.Source.Locator)
	if err != nil || ContentHash(sliced) != e.ContentHash {
		// A moved or rewritten range: retire the claim rather than
		// return divergent bytes (R-21.78). Invalidate is best-effort --
		// ErrClaimInvalidated (already retired) is not itself a failure
		// of this fetch.
		if invalidateErr := f.store.Invalidate(ctx, e.ClaimID); invalidateErr != nil && !cascade.HasKind(invalidateErr, cascade.KindConflict) {
			return nil, invalidateErr
		}
		return nil, ErrEvidenceStale
	}
	return sliced, nil
}

func (f *Fetcher) fetchCaptured(ctx context.Context, e Evidence) ([]byte, error) {
	h, err := parseCapturedRef(e.CapturedRef)
	if err != nil {
		return nil, err
	}
	rc, err := f.blobs.Get(ctx, "evidence", h)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: fetch captured content")
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: read captured content")
	}
	return data, nil
}

func parseCapturedRef(ref string) (provider.Hash, error) {
	prefix := artifactScheme + artifactIDPrefix
	if !strings.HasPrefix(ref, prefix) {
		return provider.Hash{}, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed captured_ref %q", ref)
	}
	raw, err := hex.DecodeString(ref[len(prefix):])
	if err != nil || len(raw) != provider.HashSize {
		return provider.Hash{}, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed captured_ref %q", ref)
	}
	var h provider.Hash
	copy(h[:], raw)
	return h, nil
}

func gitShow(ctx context.Context, repoPath, commit, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "show", commit+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence: git show %s:%s", commit, path)
	}
	return out, nil
}

// sliceContent extracts loc's sub-range from data. LocatorArtifact returns
// data unchanged: an artifact source's captured bytes already ARE the
// exact observed range (capture.go narrows at capture time, not at
// fetch time).
func sliceContent(data []byte, loc Locator) ([]byte, error) {
	switch loc.Kind {
	case LocatorBytes:
		if loc.Start > len(data) || loc.End > len(data) {
			return nil, errOutOfRange
		}
		return data[loc.Start:loc.End], nil
	case LocatorLines:
		lines := strings.Split(string(data), "\n")
		if loc.Start < 1 || loc.End > len(lines) {
			return nil, errOutOfRange
		}
		return []byte(strings.Join(lines[loc.Start-1:loc.End], "\n")), nil
	case LocatorArtifact:
		return data, nil
	default:
		return nil, ErrInvalidLocator
	}
}
