// Purpose: `cascade recall index verify` — a consistency report between
// the catalog document and both index legs, plus the R-21.189 generation
// marker's drift status.
//
// Inputs: the catalog document at Manager.catalogPath, the FTS5 leg's
// document keys (read directly through provider.Store.Scan — see
// store.go's docKeyPrefix doc comment for why that is the FTS5 leg's own
// published contract, not a private peek), and — when a vector store is
// configured — its per-namespace Count.
//
// Outputs: a VerifyReport, or a pkg/cascade taxonomy error when the
// catalog or the store cannot be read at all (a report with every field
// empty is a clean bill of health; a read failure is a different thing
// and must never be silently folded into one).
//
// Constraints: read-only — Verify never writes to the catalog, the index,
// or the vector store. Vector-leg completeness is checked at the
// namespace-count granularity (VectorStore's public contract has no
// per-id existence check), not per chunk id; this is a disclosed
// limitation, not a silent gap (see the CorpusVectorCount doc comment).
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
package lifecycle

import (
	"context"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MarkerCurrent and MarkerDrifted are VerifyReport.MarkerStatus's two
// values. There is no third "absent" value: R-21.189 requires an absent
// or unparseable marker to read as drifted, never as current.
const (
	MarkerCurrent = "current"
	MarkerDrifted = "drifted"
)

// VerifyReport is one verify run's consistency findings.
type VerifyReport struct {
	// Missing are catalog record chunk ids with no FTS5 document.
	Missing []string
	// Orphaned are FTS5 document chunk ids with no catalog record.
	Orphaned []string
	// VectorIncomplete names corpora whose vector-namespace count does
	// not match their catalog record count. Empty (never nil) when no
	// vector store is configured — a build with no embedder is a
	// supported degradation, not an incompleteness.
	VectorIncomplete []string
	// MarkerStatus is MarkerCurrent or MarkerDrifted.
	MarkerStatus string
	// StoredMarker is the persisted marker, "" if absent/unparseable.
	StoredMarker string
	// CurrentMarker is the tree hash Verify computed for this run.
	CurrentMarker string
}

// Clean reports whether the run found nothing to fix.
func (r VerifyReport) Clean() bool {
	return len(r.Missing) == 0 && len(r.Orphaned) == 0 &&
		len(r.VectorIncomplete) == 0 && r.MarkerStatus == MarkerCurrent
}

// Verify compares the catalog document against both index legs and the
// generation marker.
func (m *Manager) Verify(ctx context.Context) (VerifyReport, error) {
	doc, exists, err := readCatalog(m.catalogPath)
	if err != nil {
		return VerifyReport{}, err
	}
	if !exists {
		return VerifyReport{}, cascade.New(cascade.KindNotFound,
			"lifecycle: no retrieval index has been built yet; run `cascade recall index rebuild`")
	}
	recordIDs := make(map[string]bool, len(doc.Records))
	byCorpus := make(map[string]int)
	for _, r := range doc.Records {
		recordIDs[r.ID] = true
		byCorpus[r.CorpusID]++
	}
	indexedIDs, err := m.scanIndexedIDs(ctx)
	if err != nil {
		return VerifyReport{}, err
	}
	report := VerifyReport{
		Missing:          diffIDs(recordIDs, indexedIDs),
		Orphaned:         diffIDs(indexedIDs, recordIDs),
		VectorIncomplete: m.vectorIncomplete(ctx, byCorpus),
	}
	m.checkMarker(ctx, &report)
	return report, nil
}

// scanIndexedIDs enumerates the FTS5 leg's document keys directly through
// the store, per this file's doc comment.
func (m *Manager) scanIndexedIDs(ctx context.Context) (map[string]bool, error) {
	it, err := m.store.Scan(ctx, retrieval.IndexNamespace, docKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: scan retrieval index")
	}
	defer func() { _ = it.Close() }()
	ids := make(map[string]bool)
	for it.Next(ctx) {
		ids[strings.TrimPrefix(it.Key(), docKeyPrefix)] = true
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: scan retrieval index")
	}
	return ids, nil
}

// vectorIncomplete names corpora whose vector namespace count differs
// from their catalog record count. Nil (rendered empty by the caller)
// when no vector store is configured.
func (m *Manager) vectorIncomplete(ctx context.Context, byCorpus map[string]int) []string {
	if m.vectors == nil {
		return []string{}
	}
	var out []string
	for corpusID, want := range byCorpus {
		got, err := m.vectors.Count(ctx, fusion.NamespaceFor(corpusID))
		if err != nil || got != want {
			out = append(out, corpusID)
		}
	}
	sort.Strings(out)
	return out
}

// checkMarker fills in report's marker fields.
func (m *Manager) checkMarker(ctx context.Context, report *VerifyReport) {
	stored, ok := readMarker(ctx, m.store)
	current, err := m.treeHash(ctx)
	if err != nil {
		report.MarkerStatus = MarkerDrifted
		return
	}
	report.CurrentMarker = current
	if !ok {
		report.MarkerStatus = MarkerDrifted
		return
	}
	report.StoredMarker = stored.TreeHash
	if stored.TreeHash != current {
		report.MarkerStatus = MarkerDrifted
		return
	}
	report.MarkerStatus = MarkerCurrent
}
