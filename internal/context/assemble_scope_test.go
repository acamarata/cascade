package context

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: TestContextAssemblyScopeLeak proves R-16.4 end to end through
//   Assemble, using the SAME real components production wiring would use
//   (corpus.Store, fusion.NewScopeFilter) rather than a hand-rolled
//   authorization stand-in: a Project1 session's retrieval slot excludes a
//   Project3 record that shares vocabulary with Project1's own content,
//   until Project1's membership declares an edge to Project3, at which
//   point the same record is legitimately included.
// SPORT: context-engine/assembly (ADD, per T-1 sport_updates); R-16.4.

// scopedStore builds a two-corpus, two-record fixture: one corpus/record
// pair owned by "project:one", one owned by "project:three", both
// VisibilityShared so an edge (and only an edge) can reach across them.
func scopedStore(t *testing.T) *corpus.Store {
	t.Helper()
	store := corpus.NewStore()
	corpora := []corpus.Corpus{
		{ID: "proj1-docs", ScopeRef: "project:one", Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityShared, Trust: corpus.TrustTrusted},
		{ID: "proj3-docs", ScopeRef: "project:three", Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityShared, Trust: corpus.TrustTrusted},
	}
	for _, c := range corpora {
		if err := store.AddCorpus(c); err != nil {
			t.Fatalf("AddCorpus(%s): %v", c.ID, err)
		}
	}
	records := []corpus.Record{
		{ID: "chunk-in-1", CorpusID: "proj1-docs", ScopeRef: "project:one", Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityShared, Trust: corpus.TrustTrusted},
		{ID: "chunk-in-3", CorpusID: "proj3-docs", ScopeRef: "project:three", Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityShared, Trust: corpus.TrustTrusted},
	}
	for _, r := range records {
		if err := store.AddRecord(r); err != nil {
			t.Fatalf("AddRecord(%s): %v", r.ID, err)
		}
	}
	return store
}

// scopedFusedResults returns two candidates that share vocabulary (the
// scenario a naive keyword-only filter could conflate): one owned by
// project:one, one by project:three, both already ranked as if a query
// matched both.
func scopedFusedResults() []rrf.FusedResult {
	return []rrf.FusedResult{
		{ChunkID: "chunk-in-1", Path: "one/shared-term.md", CorpusID: "proj1-docs", Trust: corpus.TrustTrusted, Score: 0.9, RawScore: 2, Strategies: []rrf.StrategyName{rrf.StrategyFTS}},
		{ChunkID: "chunk-in-3", Path: "three/shared-term.md", CorpusID: "proj3-docs", Trust: corpus.TrustTrusted, Score: 0.8, RawScore: 1.5, Strategies: []rrf.StrategyName{rrf.StrategyFTS}},
	}
}

func chunkIDsOf(asm provider.ContextAssembly) []string {
	out := make([]string, len(asm.Retrieval))
	for i, c := range asm.Retrieval {
		out[i] = c.ChunkID
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// retrievalBudget allocates the whole token budget to the retrieval slot,
// so these fixtures test scope authorization rather than budget truncation.
func retrievalBudget() provider.BudgetConfig {
	return provider.BudgetConfig{MaxTokens: 1000, RetrievalRatio: 1}
}

// assembleForProject1 builds project:one's ScopeFilter (with edges, if any)
// and runs Assemble over the shared two-project fixture, failing the test
// on any setup or Assemble error so each subtest body is pure assertion.
func assembleForProject1(t *testing.T, store *corpus.Store, ranked []rrf.FusedResult, edges []corpus.Edge) provider.ContextAssembly {
	t.Helper()
	filter, err := fusion.NewScopeFilter(store, corpus.Query{
		Membership:  corpus.Membership{Scope: "project:one", Chain: []corpus.ScopeRef{"project:one"}, Edges: edges},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	scopeVal := scope.SessionScope{Kind: scope.ScopeKindSession, Session: "s1", Project: "one"}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: scopeVal, Merged: mergedWith(), Ranked: ranked, Resolver: filter,
		Counter: fixedTokenCounter{n: 10}, Budget: retrievalBudget(),
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	return asm
}

func TestContextAssemblyScopeLeak(t *testing.T) {
	store := scopedStore(t)
	ranked := scopedFusedResults()

	t.Run("without a declared edge, project1 never receives project3's record", func(t *testing.T) {
		asm := assembleForProject1(t, store, ranked, nil)
		ids := chunkIDsOf(asm)
		if contains(ids, "chunk-in-3") {
			t.Fatalf("Retrieval = %v, must NEVER contain project3's chunk-in-3 absent a declared edge (R-16.4)", ids)
		}
		if !contains(ids, "chunk-in-1") {
			t.Errorf("Retrieval = %v, want project1's own chunk-in-1 present", ids)
		}
		for _, d := range asm.Dropped {
			if d.Detail == "chunk-in-3" {
				t.Errorf("Dropped = %+v must never name a scope-withheld chunk (that is itself a disclosure)", asm.Dropped)
			}
		}
	})

	t.Run("with a declared shares_context_with edge, project1 legitimately receives project3's record", func(t *testing.T) {
		edges := []corpus.Edge{{Kind: corpus.EdgeSharesContextWith, Target: "project:three"}}
		asm := assembleForProject1(t, store, ranked, edges)
		ids := chunkIDsOf(asm)
		if !contains(ids, "chunk-in-1") || !contains(ids, "chunk-in-3") {
			t.Fatalf("Retrieval = %v, want BOTH chunk-in-1 and chunk-in-3 once the edge is declared", ids)
		}
	})
}

// TestContextAssemblyScopeLeakGeneralScopeNeverRetrieves proves the R-16.3
// restriction holds even when the injected resolver WOULD authorize
// content: a ScopeKindGeneral session (an unresolved cwd) gets no
// retrieval content at all, never a fallback to an unscoped query.
func TestContextAssemblyScopeLeakGeneralScopeNeverRetrieves(t *testing.T) {
	store := scopedStore(t)
	filter, err := fusion.NewScopeFilter(store, corpus.Query{
		Membership:  corpus.Membership{Scope: "project:one", Chain: []corpus.ScopeRef{"project:one"}},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	generalScope := scope.SessionScope{Kind: scope.ScopeKindGeneral}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: generalScope, Merged: mergedWith(), Ranked: scopedFusedResults(), Resolver: filter,
		Counter: fixedTokenCounter{n: 10}, Budget: retrievalBudget(),
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Retrieval) != 0 {
		t.Fatalf("Retrieval = %+v, want empty for a ScopeKindGeneral session (R-16.3), even with an authorizing resolver", asm.Retrieval)
	}
}
