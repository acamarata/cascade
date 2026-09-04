// Purpose: FileCatalog's tests — the four states a catalog can be in, and
// the property that matters most about them: an index that was never
// built, one that cannot be read and one that is damaged are three
// DIFFERENT answers, and none of them is an empty result set.
//
// Constraints: Art.7 — real files under t.TempDir(), no network, no wall
// clock.
// SPORT: internal.retrieval.recall.FileCatalog/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestFileCatalogLoadsARealDocument(t *testing.T) {
	index, err := NewFileCatalog(writeCatalog(t)).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if index == nil || index.Store == nil {
		t.Fatal("a loaded catalog carried no corpus model")
	}
	records, err := index.Store.Query(corpus.Query{
		Membership:  corpus.Membership{Scope: "project/cascade"},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("the rebuilt model authorized %d handbook records, want 3", len(records))
	}
}

func TestFileCatalogAnUnbuiltIndexIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), CatalogFileName)
	_, err := NewFileCatalog(path).Load(context.Background())
	assertKind(t, err, cascade.KindNotFound)
}

func TestFileCatalogADamagedDocumentIsIntegrity(t *testing.T) {
	cases := map[string][]byte{
		"not json":            []byte("{ this is not json"),
		"unclassified corpus": []byte(`{"version":1,"corpora":[{"id":"a"}],"records":[]}`),
		"orphan record": []byte(`{"version":1,"corpora":[],"records":[` +
			`{"id":"r","corpus_id":"missing","scope_ref":"s","privacy":"project",` +
			`"visibility":"scope-local","trust":"trusted"}]}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewFileCatalog(writeCatalogBytes(t, body)).Load(context.Background())
			assertKind(t, err, cascade.KindIntegrity)
		})
	}
}

func TestFileCatalogAFutureVersionIsUnsupported(t *testing.T) {
	path := writeCatalogDoc(t, CatalogDoc{Version: CatalogVersion + 1})
	_, err := NewFileCatalog(path).Load(context.Background())
	assertKind(t, err, cascade.KindUnsupported)
}

// TestFileCatalogAnUnreadableDocumentIsUnavailable covers the third state:
// the index exists and this process may not read it. That is neither
// "never built" nor "damaged", and a user chasing a permissions problem
// needs it to say so.
func TestFileCatalogAnUnreadableDocumentIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not deny reads to the owner on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless of its mode")
	}
	path := writeCatalogBytes(t, []byte(`{"version":1}`))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, err := NewFileCatalog(path).Load(context.Background())
	assertKind(t, err, cascade.KindUnavailable)
}

func TestFileCatalogHonoursACanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFileCatalog(writeCatalog(t)).Load(ctx)
	assertKind(t, err, cascade.KindCanceled)
}
