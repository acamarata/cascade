package lifecycle

// Purpose: this package's own bookkeeping over provider.Store — the
// per-corpus chunk-id manifest and the R-21.189 generation marker — plus
// the catalog document reader/writer both rebuild and update share.
//
// WHY A MANIFEST. corpus.Record (the catalog's own row type) carries no
// Path field by design (its doc comment: "the chunk text, its index rows
// and its embeddings live in the ingest, index and embed paths"). update
// needs exactly that mapping — which chunk ids came from which path — to
// retract a deleted file's chunks and leave every other file's chunks
// untouched. Rather than widening corpus.Record (out of this ticket's
// files_scope and a decision for whichever ticket owns that model), this
// package keeps its own small path->chunk-id manifest, one per corpus,
// under a key prefix ("lifecycle:manifest:") private to this package.
//
// Constraints: every key lives in retrieval.IndexNamespace (the ratified
// retrieval domain, R-14.5) under a "lifecycle:" prefix so it can never
// collide with the FTS5 leg's own "fts:" keys documented in
// internal/retrieval/fts5_schema.go.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	manifestPrefix = "lifecycle:manifest:"
	corporaKey     = "lifecycle:corpora"
	markerKey      = "lifecycle:generation"
	// docKeyPrefix is the FTS5 leg's document-key prefix, as documented
	// by internal/retrieval/fts5_schema.go's package doc comment ("A
	// document key is 'fts:doc:<chunkID>'"). Duplicated here rather than
	// imported because fts5_schema.go's own docPrefix is unexported and
	// internal/retrieval/fts5_schema.go is outside this ticket's
	// files_scope; the key layout is that file's own published contract
	// (its doc comment states the format explicitly), not a private
	// implementation detail this package reaches into. See the journal.
	docKeyPrefix = "fts:doc:"
)

// manifestEntry maps one file path to the chunk ids it produced.
type manifestEntry struct {
	Path     string   `json:"path"`
	ChunkIDs []string `json:"chunk_ids"`
}

// generationMarker is the R-21.189 marker as persisted.
type generationMarker struct {
	TreeHash  string `json:"tree_hash"`
	UpdatedAt int64  `json:"updated_at"`
}

// readManifest returns corpusID's manifest, or (nil, false, nil) when
// none has been recorded yet.
func readManifest(ctx context.Context, store provider.Store, corpusID string) ([]manifestEntry, bool, error) {
	data, err := store.Get(ctx, retrieval.IndexNamespace, manifestPrefix+corpusID)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, false, nil
		}
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: read manifest")
	}
	var entries []manifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, false, cascade.Wrap(cascade.KindIntegrity, err, "lifecycle: manifest is not readable JSON")
	}
	return entries, true, nil
}

// allManifestIDs returns the union of every chunk id recorded across
// every known corpus's manifest.
func (m *Manager) allManifestIDs(ctx context.Context) (map[string]bool, error) {
	corpusIDs, err := m.knownCorpora(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	for _, id := range corpusIDs {
		entries, ok, err := readManifest(ctx, m.store, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, e := range entries {
			for _, cid := range e.ChunkIDs {
				ids[cid] = true
			}
		}
	}
	return ids, nil
}

// knownCorpora returns the corpus ids this package has ever written a
// manifest for.
func (m *Manager) knownCorpora(ctx context.Context) ([]string, error) {
	data, err := m.store.Get(ctx, retrieval.IndexNamespace, corporaKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: read corpus index")
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "lifecycle: corpus index is not readable JSON")
	}
	return ids, nil
}

// replaceManifests overwrites every corpus's manifest from plan and drops
// the manifest of any corpus plan no longer names (a source deregistered
// entirely).
func (m *Manager) replaceManifests(ctx context.Context, plan sourcePlan) error {
	prevCorpora, err := m.knownCorpora(ctx)
	if err != nil {
		return err
	}
	nowNamed := make(map[string]bool, len(plan.corpora))
	nowIDs := make([]string, 0, len(plan.corpora))
	for _, c := range plan.corpora {
		nowNamed[c.ID] = true
		nowIDs = append(nowIDs, c.ID)
		entries := manifestFor(plan.byID[c.ID].chunks)
		data, err := json.Marshal(entries)
		if err != nil {
			return cascade.Wrap(cascade.KindInternal, err, "lifecycle: encode manifest")
		}
		if err := m.store.Put(ctx, retrieval.IndexNamespace, manifestPrefix+c.ID, data); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: write manifest")
		}
	}
	for _, id := range prevCorpora {
		if !nowNamed[id] {
			if err := m.store.Delete(ctx, retrieval.IndexNamespace, manifestPrefix+id); err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: drop manifest")
			}
		}
	}
	sort.Strings(nowIDs)
	data, err := json.Marshal(nowIDs)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "lifecycle: encode corpus index")
	}
	if err := m.store.Put(ctx, retrieval.IndexNamespace, corporaKey, data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: write corpus index")
	}
	return nil
}

// putManifest writes corpusID's manifest verbatim, without touching the
// "lifecycle:corpora" index (the caller already knows corpusID is known —
// update.go's callers only ever reach this for a corpus a prior rebuild
// or update already registered).
func (m *Manager) putManifest(ctx context.Context, corpusID string, entries []manifestEntry) error {
	if entries == nil {
		entries = []manifestEntry{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "lifecycle: encode manifest")
	}
	if err := m.store.Put(ctx, retrieval.IndexNamespace, manifestPrefix+corpusID, data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: write manifest")
	}
	return nil
}

// manifestFor groups chunks by path into manifest entries, sorted by path.
func manifestFor(chunks []retrieval.Chunk) []manifestEntry {
	byPath := make(map[string][]string)
	for _, c := range chunks {
		byPath[c.Path] = append(byPath[c.Path], c.ID)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]manifestEntry, 0, len(paths))
	for _, p := range paths {
		ids := byPath[p]
		sort.Strings(ids)
		out = append(out, manifestEntry{Path: p, ChunkIDs: ids})
	}
	return out
}

// retract removes ids from the FTS5 leg and, when a vector store is
// configured, from every corpus namespace this package knows about.
// provider.VectorStore.Delete on an id absent from a namespace is a
// documented no-op, so scanning every known namespace is harmless.
func (m *Manager) retract(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := m.index.Delete(ctx, ids); err != nil {
		return err
	}
	if m.vectors == nil {
		return nil
	}
	corpusIDs, err := m.knownCorpora(ctx)
	if err != nil {
		return err
	}
	for _, cid := range corpusIDs {
		if err := m.vectors.Delete(ctx, fusion.NamespaceFor(cid), ids); err != nil {
			return err
		}
	}
	return nil
}

// readMarker returns the persisted generation marker. ok is false when
// absent or unparseable — the fail-closed states R-21.189 requires
// Verify to treat identically as DRIFTED, never as current.
func readMarker(ctx context.Context, store provider.Store) (generationMarker, bool) {
	data, err := store.Get(ctx, retrieval.IndexNamespace, markerKey)
	if err != nil {
		return generationMarker{}, false
	}
	var marker generationMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.TreeHash == "" {
		return generationMarker{}, false
	}
	return marker, true
}

// writeMarker persists the generation marker.
func writeMarker(ctx context.Context, store provider.Store, hash string, now time.Time) error {
	data, err := json.Marshal(generationMarker{TreeHash: hash, UpdatedAt: now.Unix()})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "lifecycle: encode generation marker")
	}
	if err := store.Put(ctx, retrieval.IndexNamespace, markerKey, data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: write generation marker")
	}
	return nil
}

// writeCatalogFrom serializes plan's corpora/records to the catalog
// document at m.catalogPath, atomically (write to a temp file, then
// rename) so a concurrent reader never observes a half-written document.
func (m *Manager) writeCatalogFrom(plan sourcePlan) error {
	doc := recall.CatalogDoc{Version: recall.CatalogVersion, Corpora: plan.corpora, Records: plan.records}
	if doc.Corpora == nil {
		doc.Corpora = []corpus.Corpus{}
	}
	if doc.Records == nil {
		doc.Records = []corpus.Record{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "lifecycle: encode catalog")
	}
	if err := os.MkdirAll(filepath.Dir(m.catalogPath), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: create catalog directory")
	}
	tmp := m.catalogPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { //nolint:gosec // catalog is not secret content
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: write catalog")
	}
	if err := os.Rename(tmp, m.catalogPath); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: publish catalog")
	}
	return nil
}

// readCatalog reads and decodes the catalog document, reporting whether
// one exists.
func readCatalog(path string) (recall.CatalogDoc, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the resolved index location
	if err != nil {
		if os.IsNotExist(err) {
			return recall.CatalogDoc{}, false, nil
		}
		return recall.CatalogDoc{}, false, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: read catalog")
	}
	var doc recall.CatalogDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return recall.CatalogDoc{}, false, cascade.Wrap(cascade.KindIntegrity, err, "lifecycle: catalog is not readable JSON")
	}
	return doc, true, nil
}
