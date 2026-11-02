package fusion

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestVectorLegWithholdsPathOutsideCorpus(t *testing.T) {
	isolateVectorPathHome(t)
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "absolute.md"), "../outside.md", "docs/../../outside.md", ".."} {
		t.Run(path, func(t *testing.T) {
			checkVectorPath(t, path, root, false, "")
		})
	}
	t.Run("unresolved corpus root", func(t *testing.T) {
		checkVectorPath(t, "docs/inside.md", "", false, "")
	})
}

func TestVectorLegKeepsPathInsideCorpus(t *testing.T) {
	isolateVectorPathHome(t)
	root := t.TempDir()
	for _, path := range []string{"docs/inside.md", "docs/../inside.md", "inside.md"} {
		t.Run(path, func(t *testing.T) {
			checkVectorPath(t, path, root, true, filepath.Clean(path))
		})
	}
}

func isolateVectorPathHome(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
}

func checkVectorPath(t *testing.T, path, root string, keep bool, want string) {
	t.Helper()
	filter, p1 := project1Filter(t)
	withheld := -1
	store := &fakeVectorStore{byNamespace: map[string][]provider.VectorMatch{
		NamespaceFor(p1.Corpus.ID): {{ID: p1.Records[0].ID, Score: 1,
			Metadata: map[string]any{MetadataKeyPath: path}}},
	}}
	leg := NewVectorLegWith(&fakeEmbedder{}, store, nil, VectorLegOptions{
		CorpusRoot: func(id string) (string, bool) {
			if id != p1.Corpus.ID {
				t.Fatalf("root resolved for %q, want %q", id, p1.Corpus.ID)
			}
			return root, root != ""
		},
		Withheld: func(n int) { withheld = n },
	})
	list, ran, err := leg.Query(context.Background(), filter, "query", 10)
	if err != nil || !ran {
		t.Fatalf("Query: ran=%t err=%v", ran, err)
	}
	if keep {
		if len(list.Hits) != 1 || list.Hits[0].Path != want || withheld != 0 {
			t.Fatalf("hits=%+v withheld=%d, want path %q", list.Hits, withheld, want)
		}
	} else if len(list.Hits) != 0 || withheld != 1 {
		t.Fatalf("hits=%+v withheld=%d, want one withheld hit", list.Hits, withheld)
	}
}

func TestCandidatePathKeepsSlashForm(t *testing.T) {
	got, ok := candidatePath(Record{}, "docs/sub/../retrieval.md", nil)
	if !ok || got != "docs/retrieval.md" {
		t.Fatalf("candidatePath = %q, %v; want the slash form docs/retrieval.md", got, ok)
	}
}
