package retrieval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/providers/sqlite"
)

// storeScopeChunks are the files index's chunks: two in the owner bucket
// (memory.DefaultScopeRef, where configured sources are filed), one of
// global and one of project privacy, and one in project "proj1".
var storeScopeChunks = map[string]string{"c-local": memory.DefaultScopeRef, "c-localproj": memory.DefaultScopeRef, "c-proj": "proj1"}

// storeScopePrivacy is each chunk's corpus privacy; c-local is global.
var storeScopePrivacy = map[string]corpus.PrivacyClass{
	"c-local": corpus.PrivacyGlobal, "c-localproj": corpus.PrivacyProject, "c-proj": corpus.PrivacyProject,
}

// everyChunkLeg returns every chunk in the index, in scope or not, so any
// exclusion seen in a result comes from recall.Service's own scope filter
// and authorize pass, never from this leg.
type everyChunkLeg struct{}

func (everyChunkLeg) Query(context.Context, *fusion.ScopeFilter, string, int) (rrf.RankedList, bool, error) {
	ids := make([]string, 0, len(storeScopeChunks))
	for id := range storeScopeChunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := rrf.RankedList{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight}
	for _, id := range ids {
		out.Hits = append(out.Hits, rrf.Candidate{ChunkID: id, Path: id + ".md", CorpusID: "corpus-" + id, Trust: corpus.TrustTrusted})
	}
	return out, true, nil
}

// realFilesLeg builds a REAL recall.Service over an on-disk catalog that
// holds both chunks, each in its own corpus under its own scope.
func realFilesLeg(t *testing.T) *recall.Service {
	t.Helper()
	doc := recall.CatalogDoc{Version: recall.CatalogVersion}
	for id, ref := range storeScopeChunks {
		c := corpus.Corpus{ID: "corpus-" + id, ScopeRef: corpus.ScopeRef(ref), Privacy: storeScopePrivacy[id],
			Visibility: corpus.VisibilityPrivate, Trust: corpus.TrustTrusted}
		doc.Corpora = append(doc.Corpora, c)
		doc.Records = append(doc.Records, corpus.Record{ID: id, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	path := filepath.Join(t.TempDir(), recall.CatalogFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	svc, err := recall.NewService(recall.NewFileCatalog(path), rrf.Params{}, everyChunkLeg{})
	if err != nil {
		t.Fatalf("recall.NewService: %v", err)
	}
	return svc
}

// realMemoryLeg builds a REAL memory projection over a file store holding
// one record in the owner bucket (where `cascade memory remember` files
// them) and one in proj1, so the projection's own scope filter and the
// leg's post-filter both run against real rows.
func realMemoryLeg(t *testing.T) *memory.ProjectionJob {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	files := memory.NewFileStore(filepath.Join(dir, "memory"), testClock())
	driver, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	rows := []struct {
		name, ref string
		kind      memory.MemoryKind
	}{
		{"m-local", memory.DefaultScopeRef, memory.KindUser}, {"m-pref", memory.DefaultScopeRef, memory.KindFeedback},
		{"m-localproj", memory.DefaultScopeRef, memory.KindProject}, {"m-localref", memory.DefaultScopeRef, memory.KindReference},
		{"m-proj", "proj1", memory.KindProject},
	}
	for _, r := range rows {
		name := r.name
		if err := files.Write(ctx, memory.MemoryEntry{
			Name: name, Kind: r.kind, Body: "pelican", ScopeRef: r.ref,
			Provenance: memory.Provenance{Origin: memory.OriginSession},
		}); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	job := memory.NewProjectionJob(files, driver, nil, nil, testClock())
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("projection Run: %v", err)
	}
	return job
}

// storeScopeQuery runs one recall.what over the real files leg and the
// real memory projection, resolved by res.
func storeScopeQuery(t *testing.T, res ScopeResolver) RecallWhatResponse {
	t.Helper()
	svc, err := NewRecallWhatService(realFilesLeg(t), nil, realMemoryLeg(t), rrf.Params{}, testClock(), res, testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "pelican", K: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return resp
}

func resultIDs(resp RecallWhatResponse) []string {
	ids := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestRecallWhatGeneralScope_ReadsOwnerBucketOnly: a general session's
// files and memory legs answer from the owner bucket's non-project content
// only (R-16.3): the global chunk and the user and feedback rows. The
// owner bucket's project-class chunk and project and reference rows stay
// out, as does everything in proj1, and the files leg reports no error. The resolved scope carries a
// stray Project on purpose: a general scope never narrows by Project
// (scopeRefFor's own rule), so that value must not open proj1.
func TestRecallWhatGeneralScope_ReadsOwnerBucketOnly(t *testing.T) {
	resp := storeScopeQuery(t, fakeScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindGeneral, Project: "proj1"}})
	want := "file:c-local,memory:feedback/m-pref,memory:user/m-local"
	if got := strings.Join(resultIDs(resp), ","); got != want {
		t.Fatalf("general session results = %s, want exactly %s (errors %v)", got, want, resp.Errors)
	}
	if len(resp.Errors) != 0 {
		t.Fatalf("general session errors = %v, want none", resp.Errors)
	}
}

// TestRecallWhatProjectScope_NeverReadsOwnerBucket: a project session
// still narrows by equality to its own project; the owner bucket's chunks
// and rows stay out.
func TestRecallWhatProjectScope_NeverReadsOwnerBucket(t *testing.T) {
	resp := storeScopeQuery(t, resolverFor("proj1"))
	want := "file:c-proj,memory:project/m-proj"
	if got := strings.Join(resultIDs(resp), ","); got != want {
		t.Fatalf("project session results = %s, want exactly %s (errors %v)", got, want, resp.Errors)
	}
}

// TestRecallWhatEmptyProjectNonGeneral_FailsClosed: a session scope with
// no Project (a session in a repository with no record) is not general. The files leg refuses it as
// an invalid scope and the memory leg returns nothing, so the query
// answers with no rows at all rather than the owner bucket.
func TestRecallWhatEmptyProjectNonGeneral_FailsClosed(t *testing.T) {
	resp := storeScopeQuery(t, fakeScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindSession, Session: "s1"}})
	if got := resultIDs(resp); len(got) != 0 {
		t.Fatalf("empty-project session results = %v, want none", got)
	}
	if e := resp.Errors[DomainFile]; !strings.Contains(e, "membership has an invalid scope reference") {
		t.Fatalf("files leg error = %q, want the invalid scope refusal", e)
	}
}

// TestMemoryOutcome_EmptyScopeMatchesNothing: the leg's own post-filter,
// tested alone. No real store can hold an empty-reference row, so a fake
// hands one over; an empty requested scope must still drop it.
func TestMemoryOutcome_EmptyScopeMatchesNothing(t *testing.T) {
	mem := &fakeMemoryLeg{rows: []memory.IndexedRecord{
		{ID: "project/m-empty", Name: "m-empty", Kind: memory.KindProject, Body: "pelican", ScopeRef: ""},
	}}
	svc, err := NewRecallWhatService(nil, nil, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	if out := svc.memoryOutcome(context.Background(), RecallWhatRequest{Query: "pelican"}, 10); len(out.list.Hits) != 0 {
		t.Fatalf("memoryOutcome(scope \"\") hits = %+v, want none", out.list.Hits)
	}
}

// TestMemoryOutcome_GeneralWithoutKindSearchRefuses: memoryOutcome alone,
// given the general flag runLegs sets from storeScopeFor (the end-to-end
// path is TestRecallWhatGeneralScope_ReadsOwnerBucketOnly). Over a leg that
// cannot narrow by kind it refuses whole; the fake's project-kind
// owner-bucket row never reaches a result.
func TestMemoryOutcome_GeneralWithoutKindSearchRefuses(t *testing.T) {
	mem := &fakeMemoryLeg{rows: []memory.IndexedRecord{
		{ID: "project/m-localproj", Name: "m-localproj", Kind: memory.KindProject, Body: "pelican", ScopeRef: memory.DefaultScopeRef},
	}}
	svc, err := NewRecallWhatService(nil, nil, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	req := RecallWhatRequest{Query: "pelican", Scope: memory.DefaultScopeRef, general: true}
	out := svc.memoryOutcome(context.Background(), req, 10)
	if out.err != errGeneralMemoryUnsupported || len(out.list.Hits) != 0 {
		t.Fatalf("memoryOutcome(general, no kind search) = err %v hits %+v, want the refusal and no hits", out.err, out.list.Hits)
	}
}

// kindIgnoringLeg claims the kind-narrowed search but ignores the kinds.
type kindIgnoringLeg struct{ fakeMemoryLeg }

func (l *kindIgnoringLeg) SearchInScopeKindsIncludingExpired(
	context.Context, string, string, []memory.MemoryKind, int,
) ([]memory.IndexedRecord, error) {
	return l.rows, l.err
}

// TestMemoryOutcome_GeneralPostFilterDropsProjectKind: the leg's own
// post-filter, tested alone. A kind search that ignores its allowlist
// still cannot put a project-kind owner-bucket row in a general answer.
func TestMemoryOutcome_GeneralPostFilterDropsProjectKind(t *testing.T) {
	mem := &kindIgnoringLeg{fakeMemoryLeg{rows: []memory.IndexedRecord{
		{ID: "project/m-localproj", Name: "m-localproj", Kind: memory.KindProject, Body: "pelican", ScopeRef: memory.DefaultScopeRef},
		{ID: "user/m-local", Name: "m-local", Kind: memory.KindUser, Body: "pelican", ScopeRef: memory.DefaultScopeRef},
	}}}
	svc, err := NewRecallWhatService(nil, nil, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	req := RecallWhatRequest{Query: "pelican", Scope: memory.DefaultScopeRef, general: true}
	out := svc.memoryOutcome(context.Background(), req, 10)
	if len(out.list.Hits) != 1 || out.list.Hits[0].ChunkID != "memory:user/m-local" {
		t.Fatalf("general post-filter hits = %+v, want only memory:user/m-local", out.list.Hits)
	}
}
