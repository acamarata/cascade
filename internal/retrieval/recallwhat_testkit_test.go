package retrieval

// Purpose: shared test doubles and builders for the recallwhat_*_test.go
// files — split out of recallwhat_test.go purely for the 300-line cap
// (test infrastructure, not a test file itself; every _test.go file in
// this package shares it).
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type fakeFilesLeg struct {
	resp recall.Response
	err  error
}

func (f *fakeFilesLeg) Query(context.Context, recall.Request) (recall.Response, error) {
	return f.resp, f.err
}

type fakeConvLeg struct {
	matches   []RecallWhatTurnMatch
	segs      map[string][]RecallWhatSegment
	tiers     map[string]provider.SensitivityTier
	searchErr error
	tierErr   error
}

func (f *fakeConvLeg) SearchTurns(context.Context, string, RecallWhatSearchFilter) ([]RecallWhatTurnMatch, error) {
	return f.matches, f.searchErr
}
func (f *fakeConvLeg) ListSegments(_ context.Context, turnID string) ([]RecallWhatSegment, error) {
	return f.segs[turnID], nil
}
func (f *fakeConvLeg) ThreadPrivacy(_ context.Context, threadID string) (provider.SensitivityTier, error) {
	return f.tiers[threadID], f.tierErr
}

type fakeMemoryLeg struct {
	rows []memory.IndexedRecord
	err  error
}

// SearchInScopeIncludingExpired ignores the scopeRef argument: every test
// using this fake relies on memoryOutcome's own post-filter
// (recallwhat_filter.go's fail-closed r.ScopeRef != req.Scope check) to
// prove scoping, the same way it did before P1-E07-W5-S92-T1 moved the
// filter earlier in the real *memory.ProjectionJob -- the scope-BEFORE-cap
// behaviour itself is proven against a real ProjectionJob in
// recallwhat_memoryleg_test.go, not against this fake.
func (f *fakeMemoryLeg) SearchInScopeIncludingExpired(context.Context, string, string, int) ([]memory.IndexedRecord, error) {
	return f.rows, f.err
}

// fakeScopeResolver returns a canned SessionScope, so a unit test never
// touches a real scope.GraphStore/sql.DB (Art.7.1) — scope.ResolveSessionScope
// itself is proven elsewhere, by internal/context/scope's own tests; this
// ticket's job is proving recall.what CALLS it and refuses a mismatch.
type fakeScopeResolver struct {
	out scope.SessionScope
	err error
}

func (f fakeScopeResolver) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	return f.out, f.err
}

func resolverFor(project string) fakeScopeResolver {
	return fakeScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindSession, Project: project}}
}

// mapVault is a hermetic in-memory value source (mirrors
// internal/hooks/firewall_test.go's identical helper — no test here reads
// a real vault, Art.7.1).
type mapVault struct{ values map[string][]byte }

func (v *mapVault) List(context.Context) ([]string, error) {
	out := make([]string, 0, len(v.values))
	for name := range v.values {
		out = append(out, name)
	}
	return out, nil
}
func (v *mapVault) Get(_ context.Context, name string) ([]byte, error) {
	value, ok := v.values[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "test vault: %q", name)
	}
	return value, nil
}

// testEgress builds a REAL egress.Engine over egress.DefaultRegistry() —
// the actual EgressClassRecallWhat entry this ticket registered — so
// every test using it proves genuine SensitivityPass/substitution
// enforcement, never a fake standing in for the check under test.
func testEgress(t *testing.T) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), &mapVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// testClock is the fixed clock every test injects (Art.7.3).
func testClock() *testkit.FrozenClock {
	return testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// baselineLegs builds three legs that each answer one trusted, in-scope
// hit, tuned so none of recallwhat_filter.go's exclusion rules fire.
func baselineLegs() (*fakeFilesLeg, *fakeConvLeg, *fakeMemoryLeg) {
	files := &fakeFilesLeg{resp: recall.Response{
		Results: []recall.Result{{ChunkID: "f1", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.9}},
		Citations: []citations.Citation{
			{ChunkID: "f1", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Rank: 1, Score: 0.9},
		},
	}}
	conv := &fakeConvLeg{
		matches: []RecallWhatTurnMatch{{Turn: RecallWhatTurn{ID: "t1", ThreadID: "th1", Role: "user"}, Rank: 1}},
		segs:    map[string][]RecallWhatSegment{"t1": {{Content: "hello world"}}},
		tiers:   map[string]provider.SensitivityTier{"th1": provider.SensitivityPublic},
	}
	mem := &fakeMemoryLeg{rows: []memory.IndexedRecord{
		{ID: "project/n1", Name: "n1", Kind: memory.KindProject, Body: "note body", ScopeRef: "proj1"},
	}}
	return files, conv, mem
}

// newBaselineService builds a service over the given legs, resolved to
// scope "proj1", with a real egress engine.
func newBaselineService(t *testing.T, files *fakeFilesLeg, conv *fakeConvLeg, mem *fakeMemoryLeg) *RecallWhatService {
	t.Helper()
	svc, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	return svc
}

// newDefaultService builds a service over baselineLegs() -- the common
// case every test that does not need to mutate a specific leg reaches for.
func newDefaultService(t *testing.T) *RecallWhatService {
	t.Helper()
	files, conv, mem := baselineLegs()
	return newBaselineService(t, files, conv, mem)
}
