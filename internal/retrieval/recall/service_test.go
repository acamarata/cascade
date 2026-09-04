// Purpose: the recall service's behavioural tests — the scope guarantee
// at the composition seam, the ranked/cited happy path, and every refusal
// the surface can produce. The fixtures, the corpora and the one leg
// double they run against live in fixtures_test.go (300-line file cap).
//
// Constraints: Art.7 — files under t.TempDir(), no network, no wall clock.
// SPORT: internal.retrieval.recall.Service/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRecallServiceRanksAndCites(t *testing.T) {
	got, err := newTestService(t).Query(context.Background(), inScopeRequest())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Results) == 0 {
		t.Fatal("the query returned nothing")
	}
	if got.Results[0].Path != "handbook/fusion.md" {
		t.Errorf("top result is %q, want the document about fusion", got.Results[0].Path)
	}
	if got.Results[0].Rank != 1 {
		t.Errorf("top result carries rank %d, want 1", got.Results[0].Rank)
	}
	if len(got.Citations) != len(got.Results) {
		t.Errorf("%d results produced %d citations; every authorized result must be citable",
			len(got.Results), len(got.Citations))
	}
	if got.Citations[0].ChunkID != got.Results[0].ChunkID {
		t.Errorf("citation %+v does not describe its own result %+v", got.Citations[0], got.Results[0])
	}
	if !strings.Contains(got.Rendered, "handbook/fusion.md") {
		t.Errorf("rendered citations do not name the source:\n%s", got.Rendered)
	}
	if len(got.Legs) != 1 || got.Legs[0] != string(rrf.StrategyFTS) {
		t.Errorf("Legs = %v, want the one leg that ran", got.Legs)
	}
}

// TestRecallServiceScopeHolds is the property most likely to break when
// the parts are composed: each enforces scope separately, and the seam is
// where a leak would appear. The out-of-scope corpus holds the strongest
// match for the query, so nothing here passes by accident.
func TestRecallServiceScopeHolds(t *testing.T) {
	got, err := newTestService(t).Query(context.Background(), inScopeRequest())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Results) == 0 {
		t.Fatal("the query returned nothing, so the scope assertion would be vacuous")
	}
	assertNoJournalTrace(t, got)
	if got.Withheld != 0 {
		t.Errorf("Withheld = %d; the out-of-scope corpus must be excluded before ranking, "+
			"not filtered out of an answer it had already entered", got.Withheld)
	}
}

// TestRecallServiceCatchesALeakingLeg proves the last-moment
// re-resolution: a leg that returns an id it was never given contributes
// nothing to the results, nothing to the citations and nothing to the
// rendered block — only to the withheld COUNT.
func TestRecallServiceCatchesALeakingLeg(t *testing.T) {
	svc := newTestService(t, lexicalLeg{}, leakyLeg{chunkID: "c-secret"})
	got, err := svc.Query(context.Background(), inScopeRequest())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertNoJournalTrace(t, got)
	if got.Withheld != 1 {
		t.Errorf("Withheld = %d, want the one leaked row counted", got.Withheld)
	}
}

// assertNoJournalTrace fails if anything from the unauthorized corpus
// appears anywhere in the answer.
func assertNoJournalTrace(t *testing.T, got Response) {
	t.Helper()
	for _, r := range got.Results {
		if r.CorpusID == journalCorpus.ID || strings.Contains(r.Path, "secrets.md") ||
			r.ChunkID == "c-secret" {
			t.Errorf("out-of-scope result leaked into the ranking: %+v", r)
		}
	}
	for _, c := range got.Citations {
		if c.CorpusID == journalCorpus.ID || strings.Contains(c.Path, "secrets.md") {
			t.Errorf("out-of-scope citation leaked: %+v", c)
		}
	}
	if strings.Contains(got.Rendered, "quokka") || strings.Contains(got.Rendered, "secrets.md") ||
		strings.Contains(got.Rendered, "journal") {
		t.Errorf("out-of-scope content reached the rendered citations:\n%s", got.Rendered)
	}
}

// TestRecallServiceAdmitsItsOwnCorpus is the control: the same pipeline,
// asked by a session that IS in the journal's scope and IS personally
// entitled, does return the journal content. Without this, a service that
// returned nothing at all would pass the leak test.
func TestRecallServiceAdmitsItsOwnCorpus(t *testing.T) {
	got, err := newTestService(t).Query(context.Background(), Request{
		Query: testQuery, Scope: "user/journal", Entitlement: string(corpus.PrivacyPersonal),
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].CorpusID != journalCorpus.ID {
		t.Fatalf("the journal's own session did not receive its own corpus: %+v", got.Results)
	}
}

// TestRecallServiceEntitlementIsRequiredForPersonalContent: membership
// alone does not open personal-tier content.
func TestRecallServiceEntitlementIsRequiredForPersonalContent(t *testing.T) {
	got, err := newTestService(t).Query(context.Background(), Request{
		Query: testQuery, Scope: "user/journal",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Results) != 0 {
		t.Errorf("personal content was served to a project-entitled query: %+v", got.Results)
	}
}

// TestRecallServiceEmptyMatchIsNotAnError: a query that matches nothing is
// an empty answer and no error, which is what lets the CLI exit zero.
func TestRecallServiceEmptyMatchIsNotAnError(t *testing.T) {
	got, err := newTestService(t).Query(context.Background(), Request{
		Query: "kumquat marmalade", Scope: "project/cascade",
	})
	if err != nil {
		t.Fatalf("a query that matched nothing must not be an error: %v", err)
	}
	if len(got.Results) != 0 || len(got.Citations) != 0 {
		t.Errorf("an empty match returned %+v", got)
	}
	if got.Results == nil || got.Citations == nil {
		t.Error("an empty answer must carry empty slices, not nils, so its JSON is [] not null")
	}
}

func TestRecallServiceKBounds(t *testing.T) {
	svc := newTestService(t)
	got, err := svc.Query(context.Background(), Request{Query: testQuery, Scope: "project/cascade", K: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Results) != 1 {
		t.Errorf("--k 1 returned %d results", len(got.Results))
	}
	for _, k := range []int{-1, MaxK + 1} {
		_, err := svc.Query(context.Background(), Request{Query: testQuery, Scope: "project/cascade", K: k})
		assertKind(t, err, cascade.KindInvalidInput)
	}
}

func TestRecallServiceRefusals(t *testing.T) {
	svc := newTestService(t)
	cases := map[string]struct {
		req  Request
		kind cascade.Kind
	}{
		"empty query":       {Request{Scope: "project/cascade"}, cascade.KindInvalidInput},
		"blank query":       {Request{Query: "   ", Scope: "project/cascade"}, cascade.KindInvalidInput},
		"no scope":          {Request{Query: testQuery}, cascade.KindInvalidInput},
		"malformed scope":   {Request{Query: testQuery, Scope: "a scope"}, cascade.KindInvalidInput},
		"bad entitlement":   {Request{Query: testQuery, Scope: "project/cascade", Entitlement: "root"}, cascade.KindInvalidInput},
		"unknown corpus":    {Request{Query: testQuery, Scope: "project/cascade", Corpora: []string{"nope"}}, cascade.KindNotFound},
		"unreadable corpus": {Request{Query: testQuery, Scope: "project/cascade", Corpora: []string{"journal"}}, cascade.KindNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Query(context.Background(), tc.req)
			assertKind(t, err, tc.kind)
		})
	}
}

// TestRecallServiceUnknownCorpusDisclosesNothing: the refusal for a
// corpus that exists but is not readable must not differ from the one for
// a corpus that does not exist, and must not carry its content.
func TestRecallServiceUnknownCorpusDisclosesNothing(t *testing.T) {
	svc := newTestService(t)
	_, absent := svc.Query(context.Background(), Request{
		Query: testQuery, Scope: "project/cascade", Corpora: []string{"nope"}})
	_, hidden := svc.Query(context.Background(), Request{
		Query: testQuery, Scope: "project/cascade", Corpora: []string{"journal"}})
	if absent == nil || hidden == nil {
		t.Fatal("both corpus refusals must be errors")
	}
	absentShape := strings.Replace(absent.Error(), `"nope"`, "<name>", 1)
	hiddenShape := strings.Replace(hidden.Error(), `"journal"`, "<name>", 1)
	if absentShape != hiddenShape {
		t.Errorf("the two refusals differ in shape:\n%s\n%s", absentShape, hiddenShape)
	}
	if strings.Contains(hidden.Error(), "quokka") || strings.Contains(hidden.Error(), "secrets.md") {
		t.Errorf("a refusal carried out-of-scope content: %v", hidden)
	}
}

func TestRecallServiceLegStates(t *testing.T) {
	t.Run("no leg configured is unavailable, not empty", func(t *testing.T) {
		svc := newTestService(t, skippedLeg{})
		_, err := svc.Query(context.Background(), inScopeRequest())
		assertKind(t, err, cascade.KindUnavailable)
	})
	t.Run("a failing leg fails the query", func(t *testing.T) {
		svc := newTestService(t, failingLeg{})
		_, err := svc.Query(context.Background(), inScopeRequest())
		assertKind(t, err, cascade.KindUnavailable)
	})
	t.Run("a nil leg is skipped", func(t *testing.T) {
		svc := newTestService(t, nil, lexicalLeg{})
		got, err := svc.Query(context.Background(), inScopeRequest())
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got.Results) == 0 {
			t.Error("the configured leg did not run")
		}
	})
}

func TestNewServiceRefusesANilCatalog(t *testing.T) {
	_, err := NewService(nil, rrf.Params{})
	assertKind(t, err, cascade.KindInvalidInput)
}

// TestRecallServiceRefusesAnIndexWithNoModel covers the catalog contract
// breach a third-party Catalog could commit.
func TestRecallServiceRefusesAnIndexWithNoModel(t *testing.T) {
	svc, err := NewService(emptyCatalog{}, rrf.Params{}, lexicalLeg{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, qerr := svc.Query(context.Background(), inScopeRequest())
	assertKind(t, qerr, cascade.KindIntegrity)
}

// emptyCatalog returns an Index with no corpus model, which no shipped
// catalog does and every Catalog implementation must be refused for.
type emptyCatalog struct{}

func (emptyCatalog) Load(context.Context) (*Index, error) { return &Index{}, nil }

// assertKind fails unless err carries want.
func assertKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a %v error, got nil", want)
	}
	if !cascade.HasKind(err, want) {
		t.Fatalf("error %v does not carry %v", err, want)
	}
}
