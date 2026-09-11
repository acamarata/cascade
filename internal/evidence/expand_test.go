package evidence

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// setupPagingFixture creates a real git-backed claim/evidence pair whose
// content exceeds PageBound by 100 bytes, split out of the test body to
// stay under the funlen cap.
func setupPagingFixture(t *testing.T) (*Fetcher, *Store) {
	t.Helper()
	repoDir, commit := initGitRepo(t, "big.txt", strings.Repeat("x", PageBound+100)+"\n")
	f, store := newFetcher(t)
	ctx := context.Background()

	if err := store.PutClaim(ctx, Claim{
		ID: "CLM-PAGE", Statement: "s", Type: ClaimObservedFact, Confidence: 1,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	content := strings.Repeat("x", PageBound+100)
	e := Evidence{
		ID: "EVD-PAGE", ClaimID: "CLM-PAGE",
		Source: Source{Type: SourceGit, Repository: repoDir, Commit: commit, Path: "big.txt",
			Locator: Locator{Kind: LocatorBytes, Start: 0, End: len(content)}},
		ContentHash: ContentHash([]byte(content)), DataClass: DataClassInternal,
	}
	if err := store.PutEvidence(ctx, e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	return f, store
}

func TestExpandPagingDeterministic(t *testing.T) {
	f, _ := setupPagingFixture(t)
	ctx := context.Background()

	p1, err := f.Expand(ctx, "CLM-PAGE", "")
	if err != nil {
		t.Fatalf("Expand (page 1): %v", err)
	}
	if len(p1.Content) != 1 || len(p1.Content[0].Bytes) != PageBound || !p1.Content[0].Truncated {
		t.Fatalf("page 1: len=%d truncated=%v, want %d bytes truncated=true",
			len(p1.Content[0].Bytes), p1.Content[0].Truncated, PageBound)
	}
	if p1.NextPageToken == "" {
		t.Fatal("page 1 NextPageToken empty, want a continuation token")
	}

	p2, err := f.Expand(ctx, "CLM-PAGE", p1.NextPageToken)
	if err != nil {
		t.Fatalf("Expand (page 2): %v", err)
	}
	if len(p2.Content) != 1 || len(p2.Content[0].Bytes) != 100 || p2.Content[0].Truncated {
		t.Fatalf("page 2: len=%d truncated=%v, want 100 bytes truncated=false", len(p2.Content[0].Bytes), p2.Content[0].Truncated)
	}
	if p2.NextPageToken != "" {
		t.Errorf("page 2 NextPageToken = %q, want empty (packet complete)", p2.NextPageToken)
	}

	// Determinism: re-resolving page 1's token yields byte-identical bytes.
	p1Again, err := f.Expand(ctx, "CLM-PAGE", "")
	if err != nil {
		t.Fatalf("Expand (page 1 again): %v", err)
	}
	if string(p1Again.Content[0].Bytes) != string(p1.Content[0].Bytes) {
		t.Error("Expand is not deterministic across calls")
	}
}

func TestExpandMalformedToken(t *testing.T) {
	f, store := newFetcher(t)
	mustPutClaimWithEvidence(t, store, "CLM-TOK")
	ctx := context.Background()
	for _, tok := range []string{"no-colon", "EVD-TOK-EVD:notanumber", "EVD-UNKNOWN:0"} {
		if _, err := f.Expand(ctx, "CLM-TOK", tok); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Expand(token=%q) = %v, want typed invalid-input", tok, err)
		}
	}
}

func TestExpandUnknownClaim(t *testing.T) {
	f, _ := newFetcher(t)
	if _, err := f.Expand(context.Background(), "CLM-MISSING", ""); err != ErrClaimNotFound {
		t.Fatalf("Expand(unknown claim) = %v, want ErrClaimNotFound", err)
	}
}

func TestExpandClaimWithoutEvidence(t *testing.T) {
	f, store := newFetcher(t)
	ctx := context.Background()
	if err := store.PutClaim(ctx, Claim{
		ID: "CLM-BARE", Statement: "s", Type: ClaimObservedFact, Confidence: 1,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if _, err := f.Expand(ctx, "CLM-BARE", ""); err != ErrClaimWithoutEvidence {
		t.Fatalf("Expand(claim without evidence) = %v, want ErrClaimWithoutEvidence", err)
	}
}

func TestExpandInvalidatedClaimStillExpands(t *testing.T) {
	repoDir, commit := initGitRepo(t, "inv.txt", "hello\n")
	f, store := newFetcher(t)
	ctx := context.Background()
	if err := store.PutClaim(ctx, Claim{
		ID: "CLM-INV", Statement: "s", Type: ClaimObservedFact, Confidence: 1,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if err := store.PutEvidence(ctx, Evidence{
		ID: "EVD-INV", ClaimID: "CLM-INV",
		Source: Source{Type: SourceGit, Repository: repoDir, Commit: commit, Path: "inv.txt",
			Locator: Locator{Kind: LocatorLines, Start: 1, End: 1}},
		ContentHash: ContentHash([]byte("hello")), DataClass: DataClassInternal,
	}); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	if err := store.Invalidate(ctx, "CLM-INV"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	packet, err := f.Expand(ctx, "CLM-INV", "")
	if err != nil {
		t.Fatalf("Expand(invalidated claim) = %v, want a packet, not an error", err)
	}
	if packet.Claim.InvalidatedAt == nil {
		t.Error("Expand packet lost InvalidatedAt for a retired claim")
	}
}
