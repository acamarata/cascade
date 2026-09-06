package eval

// Purpose: RunKnownItem and RunSemanticParaphrase — the query loop that
//   drives the REAL S-10.T2 FTS5 leg, the REAL S-11.T1 vector leg, and the
//   REAL S-11.T1 RRF fusion over one query set, producing the FTS5-only
//   and RRF-fused ranked results metrics.go measures. This file has no
//   filesystem or SQLite dependency of its own: it consumes an already-
//   built Legs value, so the FTS5 database and vector store construction
//   (the parts that need t.TempDir()) stay in the caller — the harness's
//   own test, gate.go's test, and any future caller each build their own
//   real backing store the same way internal/retrieval's own tests do.
// Inputs: a QuerySet and a Legs bundle (the real FTS5 leg, the optional
//   real vector leg, the scope filter both query through, and the RRF k).
// Outputs: one RunResult per query, in the query set's order, or a
//   *cascade.Error.
// Constraints: Art.7 — no clock, no randomness, no network; every
//   collaborator here is the shipping S-10.T2/S-11.T1 implementation,
//   never a self-authored substitute (Art.2).
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RecordedVectorEmbedder implements fusion.Embedder by replaying a
// RecordedEmbeddings fixture. It never invents a vector for a text it was
// not recorded for (Art.1 — the same discipline the real bgem3 sidecar
// client documents for itself): an unrecorded query text is a hard error,
// not a zero or nearest-neighbour guess, because either would poison the
// harness's measurement rather than fail it visibly.
type RecordedVectorEmbedder struct {
	fixture RecordedEmbeddings
}

// NewRecordedVectorEmbedder builds an Embedder over fixture.
func NewRecordedVectorEmbedder(fixture RecordedEmbeddings) *RecordedVectorEmbedder {
	return &RecordedVectorEmbedder{fixture: fixture}
}

// Embed satisfies fusion.Embedder by looking up each text's recorded
// vector, in order.
func (e *RecordedVectorEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := e.fixture.Lookup(t)
		if !ok {
			return nil, cascade.Newf(cascade.KindNotFound,
				"eval: no recorded real-embedder vector for %q", t)
		}
		out[i] = v
	}
	return out, nil
}

// Legs bundles the real collaborators one evaluation run queries through.
// Vector is nil when a run evaluates the FTS5-only leg alone; a nil
// Vector is a supported degradation, not an error, exactly as it is in
// production (fusion.VectorLeg's own doc comment).
type Legs struct {
	FTS5   *retrieval.Leg
	Vector *fusion.VectorLeg
	Filter *fusion.ScopeFilter
	K      int64
}

// effectiveK resolves k to rrf.DefaultK when unset, mirroring
// rrf.Params.EffectiveK so the harness fuses under the exact constant a
// shipping caller would.
func (l Legs) effectiveK() int64 {
	if l.K <= 0 {
		return rrf.DefaultK
	}
	return l.K
}

// RunResult carries one query's FTS5-only and RRF-fused rankings, both
// capped for recall@10 by the metrics that read them.
type RunResult struct {
	FTS5Only RankedResult
	Fused    RankedResult
}

// RunKnownItem runs every query in qs through legs and returns the
// FTS5-only and fused ranked results, in qs's order.
func RunKnownItem(ctx context.Context, qs QuerySet, legs Legs) ([]RunResult, error) {
	return run(ctx, qs, legs)
}

// RunSemanticParaphrase is RunKnownItem's sibling for the
// semantic/paraphrase query set. The query path is identical; the two
// verbs exist because FusionDefaultGate reasons about the two sets
// separately (R-16.9 measures the lift on semantic/paraphrase only).
func RunSemanticParaphrase(ctx context.Context, qs QuerySet, legs Legs) ([]RunResult, error) {
	return run(ctx, qs, legs)
}

// run is the shared query loop both exported entry points call.
func run(ctx context.Context, qs QuerySet, legs Legs) ([]RunResult, error) {
	out := make([]RunResult, 0, len(qs.Queries))
	for _, q := range qs.Queries {
		result, err := runOne(ctx, q, legs)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, nil
}

// runOne runs one query through both legs and fuses them.
func runOne(ctx context.Context, q Query, legs Legs) (RunResult, error) {
	ftsList, _, err := legs.FTS5.Query(ctx, legs.Filter, q.Text, topN)
	if err != nil {
		return RunResult{}, err
	}
	lists := []rrf.RankedList{ftsList}
	if legs.Vector != nil {
		vecList, ran, vecErr := legs.Vector.Query(ctx, legs.Filter, q.Text, topN)
		if vecErr != nil {
			return RunResult{}, vecErr
		}
		if ran {
			lists = append(lists, vecList)
		}
	}
	fused, err := rrf.Fuse(lists, legs.effectiveK())
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{
		FTS5Only: RankedResult{QueryText: q.Text, RankedIDs: idsFromHits(ftsList.Hits)},
		Fused:    RankedResult{QueryText: q.Text, RankedIDs: idsFromFused(fused)},
	}, nil
}

// idsFromHits reads a ranked list's chunk ids in rank order.
func idsFromHits(hits []rrf.Candidate) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ChunkID
	}
	return out
}

// idsFromFused reads a fused result set's chunk ids in rank order.
func idsFromFused(results []rrf.FusedResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.ChunkID
	}
	return out
}
