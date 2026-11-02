package daemon

// Purpose: recall.index.* with a RecallEmbedding: only corpora the
//   admission rule admits reach the embedder, a refused corpus's namespace
//   stays empty, and verify does not report it as vector-incomplete.
// Constraints: a real modernc SQLite store under t.TempDir(); no repository
//   is needed for corpus admission; the embedder records inputs without a
//   network listener.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
	"github.com/acamarata/cascade/providers/sqlite"
)

// recordingEmbedder answers with one-dimension vectors and records texts.
type recordingEmbedder struct{ texts []string }

func (r *recordingEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "daemon-test-embed", Dimensions: 1}
}

func (r *recordingEmbedder) Embed(_ context.Context, in []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	out := make([]provider.EmbedOutput, len(in))
	for i, e := range in {
		r.texts = append(r.texts, e.Text)
		out[i] = provider.EmbedOutput{Vector: []float32{float32(len(e.Text))}, Model: r.Model()}
	}
	return out, nil
}

// embedFixture registers recall.index.* over a real store with two
// configured sources (corpora "code" and "code:1") and emb.
func embedFixture(t *testing.T, emb func(provider.Store) RecallEmbedding) (*rpc.Registry, provider.Store) {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, dir)
	}
	chdir(t, dir)
	var roots []string
	for i, body := range []string{"# Shared\n\nadmitted words", "# Private\n\nrefused words"} {
		root := filepath.Join(dir, "src"+string(rune('a'+i)))
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "n.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, "'"+root+"'") // a TOML literal string: a Windows path has backslashes
	}
	paths := fakePaths{root: dir}
	if err := os.WriteFile(paths.ConfigPath(), []byte("[retrieval]\nsources = ["+strings.Join(roots, ", ")+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cascade.db")
	store, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg := rpc.NewRegistry()
	if err := RegisterRecallIndexHandler(reg, paths, runtime.NewSystemClock(), store, dbPath, emb(store)); err != nil {
		t.Fatalf("RegisterRecallIndexHandler: %v", err)
	}
	return reg, store
}

func dispatchIndex(t *testing.T, reg *rpc.Registry, method string) any {
	t.Helper()
	res, errObj := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method, ID: []byte("1")})
	if errObj != nil {
		t.Fatalf("%s: %+v", method, errObj)
	}
	return res
}

func TestRecallIndexEmbedsOnlyAdmittedCorpora(t *testing.T) {
	e := &recordingEmbedder{}
	var vectors provider.VectorStore
	reg, _ := embedFixture(t, func(s provider.Store) RecallEmbedding {
		vectors = localvector.New(s)
		return RecallEmbedding{Embedder: e, Vectors: vectors, AdmitCorpus: func(c corpus.Corpus) bool { return c.ID == corpus.CorpusIDCode }}
	})
	dispatchIndex(t, reg, RecallIndexRebuildMethod)
	joined := strings.Join(e.texts, "|")
	if !strings.Contains(joined, "admitted words") || strings.Contains(joined, "refused words") {
		t.Fatalf("embedded %q: want the admitted corpus only", joined)
	}
	ctx := context.Background()
	if n, err := vectors.Count(ctx, fusion.NamespaceFor(corpus.CorpusIDCode+":1")); err != nil || n != 0 {
		t.Fatalf("refused corpus holds %d vectors (err %v)", n, err)
	}
	report, ok := dispatchIndex(t, reg, RecallIndexVerifyMethod).(lifecycle.VerifyReport)
	if !ok || len(report.VectorIncomplete) != 0 || len(report.Missing) != 0 {
		t.Fatalf("verify = %+v (ok %t), want no vector-incomplete corpus", report, ok)
	}

	none := &recordingEmbedder{}
	reg, _ = embedFixture(t, func(s provider.Store) RecallEmbedding {
		return RecallEmbedding{Embedder: none, Vectors: localvector.New(s)}
	})
	dispatchIndex(t, reg, RecallIndexRebuildMethod)
	if len(none.texts) != 0 {
		t.Fatalf("no admission rule embedded %d texts, want none", len(none.texts))
	}
	err := RegisterRecallIndexHandler(rpc.NewRegistry(), fakePaths{root: t.TempDir()}, runtime.NewSystemClock(), &memStoreForEmbed{}, "", RecallEmbedding{}, RecallEmbedding{})
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("two embeddings: %v, want KindInvalidInput", err)
	}
}

// memStoreForEmbed is a non-nil store for the argument check only; it is
// never read because registration refuses before building a Manager.
type memStoreForEmbed struct{ provider.Store }
