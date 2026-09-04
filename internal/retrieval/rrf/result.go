// Package rrf is the ranking core of retrieval: it merges the ranked
// result lists produced by the retrieval legs into one ordered answer via
// Reciprocal Rank Fusion, normalizes the fused scores into [0,1], and
// collapses a chunk that several legs returned into exactly one result.
//
// Purpose: one deterministic place where "which chunks best answer this
// query, and in what order" is decided. Citations, the recall command
// surface and context assembly all read the ordering this package
// produces, so the ordering has to be a function of the inputs alone:
// feeding the same legs in a different order, or on a different run,
// produces byte-identical output.
//
// Inputs: one RankedList per leg, each already ordered best-first, plus
// the RRF smoothing constant k.
//
// Outputs: a []FusedResult ordered by descending fused score, each result
// carrying its normalized score, the legs that contributed to it, and the
// corpus membership and TRUST tag the scope-filtered candidate set
// resolved for it.
//
// Constraints: pure computation, no I/O and no network. Nothing here
// decides what a session may see: the candidate sets handed in were
// already narrowed by the scope filter before either leg ran, and this
// package never widens them and never re-checks them. Nothing here
// enforces the TRUST tag either; it carries the tag through intact for the
// consumer that decides whether to obey the content.
//
// SPORT: internal.retrieval.rrf.FusedResult/ADDED (P1-E06-W2-S11-T1).
package rrf

import "github.com/acamarata/cascade/internal/retrieval/corpus"

// DefaultK is the RRF smoothing constant every leg is fused with unless a
// caller passes another value: 60, the canonical constant the retrieval
// reference documents. It is a named constant rather than a literal at the
// call site so the value has exactly one home, and the documented value is
// asserted against docs/reference/retrieval.md rather than against a
// second copy of this declaration.
const DefaultK int64 = 60

// NeutralWeight is the leg weight that leaves standard RRF unchanged. A
// leg carrying it contributes 1/(k+rank) per hit, which is plain RRF; a
// heavier leg contributes proportionally more and a leg weighted 0
// contributes nothing while still being recorded as having run.
const NeutralWeight float64 = 1.0

// StrategyName identifies one retrieval leg. It is a defined type rather
// than a bare string so a leg name is self-documenting wherever it is
// recorded, including in the contributing-strategy list every result
// carries into its citation.
type StrategyName string

const (
	// StrategyFTS is the full-text index leg.
	StrategyFTS StrategyName = "fts5"
	// StrategyVector is the embedding-similarity leg.
	StrategyVector StrategyName = "vector"
)

// Candidate is one ranked hit as a leg returned it. Rank is positional:
// the first Candidate in a RankedList is rank 1.
//
// A Candidate carries the classification the scope filter already resolved
// for the chunk. It is recorded, never re-decided: a leg that returned a
// chunk it was not entitled to return would be a defect in the leg, and
// re-checking here would hide it behind a silent drop.
type Candidate struct {
	// ChunkID is the stable, content-addressed chunk identifier. It is the
	// identity fusion dedupes on: two hits carrying the same ChunkID are
	// the same content, whatever else differs about them.
	ChunkID string
	// Path is the source path the chunk was carved from. Identical content
	// at two paths shares one ChunkID by design, so Path is descriptive
	// rather than identifying and never takes part in the dedupe.
	Path string
	// CorpusID names the corpus the chunk was retrieved from.
	CorpusID string
	// Trust is the TRUST tag the scope filter resolved for the chunk.
	Trust corpus.TrustLevel
}

// RankedList is one leg's ordered output, best hit first.
type RankedList struct {
	// Strategy names the leg. Two lists may not share a name in one Fuse
	// call: the pair would be indistinguishable in a result's
	// contributing-strategy list.
	Strategy StrategyName
	// Weight multiplies every contribution this leg makes. Use
	// NeutralWeight for standard RRF.
	Weight float64
	// Hits are the leg's candidates in rank order. An empty or nil Hits is
	// a leg that ran and matched nothing, which is not an error.
	Hits []Candidate
}

// FusedResult is one chunk's place in the fused ranking.
type FusedResult struct {
	// ChunkID is the identity the result was deduped on.
	ChunkID string
	// Path is the source path, chosen deterministically when the legs
	// reported the same content at more than one path.
	Path string
	// CorpusID names the corpus the chunk came from, chosen
	// deterministically the same way as Path.
	CorpusID string
	// Trust is the effective TRUST tag: the tag the legs agreed on, or
	// corpus.TrustUntrustedSource when they did not. It rides through to
	// the consumer that decides whether to act on the content; nothing in
	// this package enforces it.
	Trust corpus.TrustLevel
	// RawScore is the fused RRF score before normalization, the value the
	// ranking is actually decided by.
	RawScore float64
	// Score is RawScore normalized into [0,1] across this result set.
	Score float64
	// Strategies are the legs that contributed to RawScore, sorted by
	// name so the field is a function of the inputs and not of the order
	// the legs were passed in.
	Strategies []StrategyName
}
