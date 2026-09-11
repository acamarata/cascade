// Package corpus (this file) registers the closed set of corpus content
// KINDS this tree defines, distinct from the scope (WHO owns it, in
// scope.go) and trust/visibility/privacy (HOW it may travel) dimensions
// model.go, trust.go and visibility.go already carry.
//
// Purpose: give "what content is this" a typed, validated identity so a
// caller's --corpus filter (recall/query.go's corpusQuery) can be checked
// against a known set rather than accepted as an arbitrary string, and so
// a query over an unrecognized kind refuses rather than silently
// returning an empty result set — the failure mode 06-FORGE-SPEC §5.20
// and this ticket's own brief both call out as indistinguishable from
// "no results" unless it is refused loudly.
//
// Inputs: a corpus id string, as it would arrive on a Corpus.ID or a
// --corpus request value.
// Outputs: ValidateCorpusKind's typed refusal for anything not in the
// registered set.
//
// Constraints: only kinds this tree has real ingestion code for are
// registered. P1-E25-W5-S52-T6 adds exactly one: CorpusIDCode. The
// "file/memory/conversation" kinds this ticket's own planning prose
// mentions as siblings do not exist as named, distinct corpus KINDS
// anywhere in the current tree (recall's Corpus filter checks a
// per-session INDEXED corpus id, not a static kind registry) — this file
// does not invent them. See the ticket's journal entry for the
// contract-vs-tree note.
//
// SPORT: internal.retrieval.corpus.CorpusIDCode/ADDED,
//
//	internal.retrieval.corpus.ValidateCorpusKind/ADDED (P1-E25-W5-S52-T6).
package corpus

import "github.com/acamarata/cascade/pkg/cascade"

// CorpusIDCode is the corpus id a git-repository source ingests under
// (code.go's IngestGitRepo). It is a plain corpus.Corpus.ID like any
// other the recall query path already accepts via --corpus; registering
// it here gives it a named constant and a validated kind rather than a
// bare string repeated at every call site.
const CorpusIDCode = "code"

// CorpusIDGraph is the corpus id the symbol/dependency graph ingests
// under (internal/retrieval's IngestSymbolGraph, P1-E33-W7-S67-T3). It
// closes the seam P1-E25-W5-S52-T6 named and deliberately left open: a
// second real ingestor alongside CorpusIDCode, sharing this same kind
// registry rather than a parallel one.
const CorpusIDGraph = "graph"

// registeredCorpusKinds is the closed set this file recognizes. Adding a
// kind here is a deliberate registration, matching a real ingestor this
// tree ships; it is never grown to make a validation pass.
var registeredCorpusKinds = map[string]bool{
	CorpusIDCode:  true,
	CorpusIDGraph: true,
}

// ValidateCorpusKind fails closed on any kind not in the registered set,
// including an empty string. A caller must never treat "unregistered" as
// "empty result set" — the two are observably different failure modes
// (one is a caller bug or a typo, the other is a real, empty index) and
// collapsing them into a silent empty response is exactly the defect
// 06-FORGE-SPEC §5.20 requires refusing.
func ValidateCorpusKind(kind string) error {
	if !registeredCorpusKinds[kind] {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus: %q is not a registered corpus kind", kind)
	}
	return nil
}
