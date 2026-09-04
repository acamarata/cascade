// Purpose: FileCatalog — the shipped Catalog: the retrieval index's corpus
// model as it is written to disk, and the reader that turns it back into a
// corpus.Store the scope filter can be resolved against.
//
// Inputs: a path to the catalog document.
// Outputs: an *Index, or a pkg/cascade taxonomy error.
//
// Constraints: the three failure modes a reader must be able to tell apart
// are kept apart. An index that was never built is KindNotFound, an index
// that cannot be read is KindUnavailable, and an index whose contents do
// not validate is KindIntegrity. None of the three is ever answered with
// an empty model, because an empty model is what "nothing matched" looks
// like, and a user deciding whether their index is broken needs those to
// be different answers.
//
// SPORT: internal.retrieval.recall.FileCatalog/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CatalogVersion is the catalog document's schema version. It is written
// by whatever builds the index and checked here, so a document from a
// future build is refused rather than half-read.
const CatalogVersion = 1

// CatalogFileName is the catalog document's name within the retrieval
// index directory.
const CatalogFileName = "catalog.json"

// CatalogDoc is the on-disk catalog: the corpora the index holds and the
// records carved into them, each carrying its own classification.
//
// Content is deliberately not here. This document is the authorization
// and provenance half of the index; the chunk text and the vectors live
// in the legs' own stores, and a catalog that carried content would put
// the whole index behind one file read.
type CatalogDoc struct {
	Version int             `json:"version"`
	Corpora []corpus.Corpus `json:"corpora"`
	Records []corpus.Record `json:"records"`
}

// FileCatalog reads the catalog document at Path.
type FileCatalog struct {
	// Path is the catalog document's location.
	Path string
}

// NewFileCatalog returns a catalog reading path.
func NewFileCatalog(path string) *FileCatalog { return &FileCatalog{Path: path} }

// Load reads the catalog and rebuilds the corpus model from it.
//
// The document is read fresh on every query rather than cached. The index
// is written by a separate process (the index-lifecycle verbs), so a
// cached model would keep answering from an index that has since been
// rebuilt, and a stale authorization decision is the one kind of stale
// this surface must not serve.
func (c *FileCatalog) Load(ctx context.Context) (*Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "recall: catalog read canceled")
	}
	raw, err := os.ReadFile(c.Path) //nolint:gosec // the path is the resolved index location
	if err != nil {
		return nil, readError(err)
	}
	var doc CatalogDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err,
			"recall: the retrieval index catalog is not readable JSON")
	}
	if doc.Version != CatalogVersion {
		return nil, cascade.Newf(cascade.KindUnsupported,
			"recall: the retrieval index catalog is version %d; this build reads version %d",
			doc.Version, CatalogVersion)
	}
	return indexFrom(doc)
}

// readError classifies a failed read of the catalog document.
func readError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return cascade.Wrap(cascade.KindNotFound, err,
			"recall: no retrieval index has been built yet")
	}
	return cascade.Wrap(cascade.KindUnavailable, err,
		"recall: the retrieval index catalog could not be read")
}

// indexFrom rebuilds the corpus model from a decoded document.
//
// Every corpus and record goes through the store's own validating writes,
// so a catalog holding an unclassified corpus or a record whose corpus is
// missing is refused as damaged rather than loaded with the offending row
// quietly dropped. A dropped row is a narrower answer that looks like a
// complete one, and on this particular document the rows ARE the
// authorization model.
func indexFrom(doc CatalogDoc) (*Index, error) {
	store := corpus.NewStore()
	for _, c := range doc.Corpora {
		if err := store.AddCorpus(c); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err,
				"recall: the retrieval index catalog holds an unusable corpus")
		}
	}
	for _, r := range doc.Records {
		if err := store.AddRecord(r); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err,
				"recall: the retrieval index catalog holds an unusable record")
		}
	}
	return &Index{Store: store}, nil
}
