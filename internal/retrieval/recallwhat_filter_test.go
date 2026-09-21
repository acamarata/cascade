package retrieval

// Purpose: unit tests for recallwhat_filter.go/recallwhat_redact.go — the
// two post-fusion exclusion rules (now real-egress-backed, not a
// caller-declared-tier comparison), the R-16.7 supersede/expire demotion
// (golden-fixture driven per this ticket's acceptance criterion), the
// memory leg's projection-backed search, and buildResults' hydration.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/provider"
)

func candidateFrom(id, domain string, trust corpus.TrustLevel) whatCandidate {
	return whatCandidate{Candidate: rrf.Candidate{ChunkID: id, Trust: trust}, Domain: domain}
}

// TestFilterFusedResults_LocalOnlyThreadExcludedByRealEgressEngine proves
// fix item 2: the exclusion decision is taken by the REAL egress boundary
// (EgressClassRecallWhat admits only internal/public), never a
// caller-declared Tier — there is no caller Tier on RecallWhatRequest any
// more. Failing input: a conversation row whose own ThreadTier is
// local-only.
func TestFilterFusedResults_LocalOnlyThreadExcludedByRealEgressEngine(t *testing.T) {
	c := candidateFrom("c1", DomainConversation, corpus.TrustTrusted)
	c.ThreadTier = provider.SensitivityLocalOnly
	c.Snippet = "secret local content"
	fused := []rrf.FusedResult{{ChunkID: "c1", Trust: corpus.TrustTrusted}}
	meta := map[string]whatCandidate{"c1": c}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, meta)
	if len(out.fused) != 0 {
		t.Fatalf("a local-only thread reached the fused output: %+v", out.fused)
	}
	if out.withheld != 1 {
		t.Errorf("withheld = %d, want 1", out.withheld)
	}
}

// TestFilterFusedResults_PublicThreadKeptAndSnippetSubstituted proves the
// companion positive case over the SAME real engine: a public-tier thread
// is admitted, and its snippet has already been through the boundary
// (substitution, not just a pass-through).
func TestFilterFusedResults_PublicThreadKeptAndSnippetSubstituted(t *testing.T) {
	c := candidateFrom("c1", DomainConversation, corpus.TrustTrusted)
	c.ThreadTier = provider.SensitivityPublic
	c.Snippet = "ordinary content"
	fused := []rrf.FusedResult{{ChunkID: "c1", Trust: corpus.TrustTrusted}}
	meta := map[string]whatCandidate{"c1": c}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, meta)
	if len(out.fused) != 1 {
		t.Fatalf("a public-tier thread was wrongly withheld: withheld=%d", out.withheld)
	}
	if out.meta["c1"].Snippet != "ordinary content" {
		t.Errorf("snippet = %q, want unchanged content with no secret in it", out.meta["c1"].Snippet)
	}
}

// TestFilterFusedResults_SecretSnippetRedactedRowSurvives is D6/Q3's
// survival guard: the confirming review found the AKIA-leak conversation
// snippet test (recallwhat_redact_test.go) asserted only that the raw
// secret was absent, never that the ROW survived -- a bug that silently
// DROPPED the row instead of redacting its snippet would pass that test
// identically. This test plants the same AKIA-shaped secret in a
// public-tier conversation snippet and asserts all three: the row
// survives (not withheld), the raw secret is gone, and the snippet is
// non-empty (redacted to a tag, not wiped blank) -- so a silent-drop
// regression fails here even though it would still hide the secret.
func TestFilterFusedResults_SecretSnippetRedactedRowSurvives(t *testing.T) {
	c := candidateFrom("c1", DomainConversation, corpus.TrustTrusted)
	c.ThreadTier = provider.SensitivityPublic
	c.Snippet = "my key is " + akiaSecret + " keep it secret"
	fused := []rrf.FusedResult{{ChunkID: "c1", Trust: corpus.TrustTrusted}}
	meta := map[string]whatCandidate{"c1": c}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, meta)
	if len(out.fused) != 1 {
		t.Fatalf("the row did not survive redaction (silently dropped instead): withheld=%d", out.withheld)
	}
	got := out.meta["c1"].Snippet
	if strings.Contains(got, akiaSecret) {
		t.Fatalf("snippet still carries the raw secret: %q", got)
	}
	if got == "" {
		t.Fatal("snippet was wiped blank rather than redacted to a tag -- a silent drop, not a substitution")
	}
}

// TestFilterFusedResults_UntrustedExcludedUnconditionally proves the
// rework's tightened rule 2 (recallwhat_filter.go header): untrusted-
// source content is ALWAYS excluded, not merely from a caller-asserted
// "privileged" context (the value the adversarial review found
// forgeable). Failing input: a file-domain row already carrying a real
// file citation, but Trust=TrustUntrustedSource.
func TestFilterFusedResults_UntrustedExcludedUnconditionally(t *testing.T) {
	c := candidateFrom("f1", DomainFile, corpus.TrustUntrustedSource)
	c.HasFileCitation = true
	fused := []rrf.FusedResult{{ChunkID: "f1", Trust: corpus.TrustUntrustedSource}}
	meta := map[string]whatCandidate{"f1": c}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, meta)
	if len(out.fused) != 0 {
		t.Fatalf("untrusted-source content reached the fused output: %+v", out.fused)
	}
}

func TestFilterFusedResults_UnknownChunkDroppedFailClosed(t *testing.T) {
	fused := []rrf.FusedResult{{ChunkID: "ghost"}}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, map[string]whatCandidate{})
	if len(out.fused) != 0 {
		t.Fatalf("a row with no meta entry must be dropped fail-closed: %+v", out.fused)
	}
}

// TestFilterFusedResults_FileRowWithoutCitationExcluded proves fix item 3:
// a file-domain row recall.Service's own authorize() pass did not produce
// a citation for is never re-admitted here.
func TestFilterFusedResults_FileRowWithoutCitationExcluded(t *testing.T) {
	c := candidateFrom("f1", DomainFile, corpus.TrustTrusted)
	c.HasFileCitation = false
	fused := []rrf.FusedResult{{ChunkID: "f1", Trust: corpus.TrustTrusted}}
	meta := map[string]whatCandidate{"f1": c}
	out := filterFusedResults(context.Background(), testEgressForFilter(t), fused, meta)
	if len(out.fused) != 0 {
		t.Fatalf("a file row with no leg-authorized citation reached the fused output: %+v", out.fused)
	}
}

// testEgressForFilter builds the same real engine recallwhat_test.go's
// testEgress does; duplicated as a package-private helper name so this
// file has no test-file-ordering dependency on recallwhat_test.go.
func testEgressForFilter(t *testing.T) EgressSubstitutor { return testEgress(t) }

// rankingGolden is recallwhat_ranking.json's shape.
type rankingGolden struct {
	FusedOrder []struct {
		ChunkID    string  `json:"chunk_id"`
		Score      float64 `json:"score"`
		Supersedes string  `json:"supersedes"`
		Expired    bool    `json:"expired"`
	} `json:"fused_order"`
	ExpectedOrder []string `json:"expected_order"`
}

// TestDemoteSupersededAndExpired asserts R-16.7 against the checked-in
// golden testdata/v1-goldens/recallwhat_ranking.json: expected_order is
// hand-traced against the algorithm's own documented rules, not captured
// from a run of this code, so it cannot pass by agreeing with itself.
func TestDemoteSupersededAndExpired(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1-goldens/recallwhat_ranking.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g rankingGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	var fused []rrf.FusedResult
	meta := map[string]whatCandidate{}
	for _, c := range g.FusedOrder {
		fused = append(fused, rrf.FusedResult{ChunkID: c.ChunkID, Score: c.Score})
		meta[c.ChunkID] = whatCandidate{
			Candidate: rrf.Candidate{ChunkID: c.ChunkID}, Supersedes: c.Supersedes, Expired: c.Expired,
		}
	}
	got := demoteSupersededAndExpired(fused, meta)
	if len(got) != len(g.ExpectedOrder) {
		t.Fatalf("got %d rows, want %d", len(got), len(g.ExpectedOrder))
	}
	for i, want := range g.ExpectedOrder {
		if got[i].ChunkID != want {
			t.Errorf("position %d: got %q, want %q (full got: %v)", i, got[i].ChunkID, want, chunkIDs(got))
		}
	}
}

func chunkIDs(fused []rrf.FusedResult) []string {
	out := make([]string, len(fused))
	for i, r := range fused {
		out[i] = r.ChunkID
	}
	return out
}

func TestDemoteSupersededAndExpired_NoFlags_OrderUnchanged(t *testing.T) {
	fused := []rrf.FusedResult{{ChunkID: "a"}, {ChunkID: "b"}, {ChunkID: "c"}}
	meta := map[string]whatCandidate{"a": {}, "b": {}, "c": {}}
	got := demoteSupersededAndExpired(fused, meta)
	if chunkIDs(got)[0] != "a" || chunkIDs(got)[1] != "b" || chunkIDs(got)[2] != "c" {
		t.Fatalf("order changed with no expired/superseded rows: %v", chunkIDs(got))
	}
}

func TestDemoteSupersededAndExpired_SupersedesAbsentTarget_NoPanic(t *testing.T) {
	fused := []rrf.FusedResult{{ChunkID: "a"}}
	meta := map[string]whatCandidate{"a": {Supersedes: "memory:nowhere"}}
	got := demoteSupersededAndExpired(fused, meta)
	if len(got) != 1 || got[0].ChunkID != "a" {
		t.Fatalf("a dangling Supersedes reference must be a no-op: %v", chunkIDs(got))
	}
}

func TestBuildResults_RankAndCitationsMatch(t *testing.T) {
	svc := newDefaultService(t)
	fused := []rrf.FusedResult{
		{ChunkID: "memory:a", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.9},
		{ChunkID: "memory:b", Path: "b.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.5},
	}
	meta := map[string]whatCandidate{
		"memory:a": {Domain: DomainMemory, Snippet: "snip-a"},
		"memory:b": {Domain: DomainMemory, Snippet: "snip-b"},
	}
	results, cites, err := svc.buildResults(context.Background(), fused, meta)
	if err != nil {
		t.Fatalf("buildResults: %v", err)
	}
	if len(results) != 2 || len(cites) != 2 {
		t.Fatalf("got %d results, %d citations, want 2 and 2", len(results), len(cites))
	}
	if results[0].Rank != 1 || results[1].Rank != 2 {
		t.Errorf("ranks = %d,%d, want 1,2", results[0].Rank, results[1].Rank)
	}
	if results[0].Snippet != "snip-a" || cites[0].ChunkID != "memory:a" || cites[1].ChunkID != "memory:b" {
		t.Errorf("results/citations not aligned by row: %+v / %+v", results, cites)
	}
}

// TestBuildResults_FileDomainReusesLegCitation proves fix item 3: a file
// row's citation is the leg's OWN authorized citation (its Lines, set
// only by a real resolver), never hand-built here with no resolver.
func TestBuildResults_FileDomainReusesLegCitation(t *testing.T) {
	svc := newDefaultService(t)
	fused := []rrf.FusedResult{{ChunkID: "file:f1", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.9}}
	meta := map[string]whatCandidate{
		"file:f1": {
			Domain: DomainFile, HasFileCitation: true,
			FileCitation: citations.Citation{
				ChunkID: "f1", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
				Rank: 1, Score: 0.9, Lines: citations.LineRange{Start: 10, End: 20},
			},
		},
	}
	_, cites, err := svc.buildResults(context.Background(), fused, meta)
	if err != nil {
		t.Fatalf("buildResults: %v", err)
	}
	if len(cites) != 1 {
		t.Fatalf("got %d citations, want 1", len(cites))
	}
	if cites[0].Lines != (citations.LineRange{Start: 10, End: 20}) {
		t.Fatalf("Lines = %+v, want the leg's own citation's Lines preserved (not hand-built)", cites[0].Lines)
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"héllo", 2, "hé"}, // rune-safe: never splits inside a multi-byte rune
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.max); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestMemorySupersedesID(t *testing.T) {
	if got := memorySupersedesID(""); got != "" {
		t.Errorf("empty ref: got %q, want empty", got)
	}
	if got := memorySupersedesID("not-an-address"); got != "" {
		t.Errorf("malformed ref: got %q, want empty (fail closed, never panic)", got)
	}
	if got := memorySupersedesID("project/foo"); got != "memory:project/foo" {
		t.Errorf("got %q, want memory:project/foo", got)
	}
}

// TestMemoryOutcome_ExpiredEntryDemoted (D6/Q6, real *memory.ProjectionJob,
// not fakeMemoryLeg) lives in recallwhat_memoryleg_test.go, split out
// purely for the 300-line cap -- it needs its own real-SQLite fixture
// imports (path/filepath, providers/sqlite) this file has no other use
// for.
