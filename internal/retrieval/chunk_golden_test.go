// Purpose: golden-fixture tests over testdata/v1-goldens/ (Art.10.3 split
// from chunk_test.go, which crossed the 300-line cap once this ticket's
// coverage-branch tests were added — the split is by concern, not by
// arbitrary line count: this file owns everything golden-fixture-shaped).
//
// SPORT: internal.retrieval.Chunk/ADDED (P1-E06-W2-S10-T1) — test coverage.
package retrieval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type goldenFile struct {
	InputPath string        `json:"input_path"`
	Input     string        `json:"input"`
	Chunks    []goldenChunk `json:"chunks"`
}

type goldenChunk struct {
	StartByte int      `json:"start_byte"`
	EndByte   int      `json:"end_byte"`
	Lang      string   `json:"lang"`
	Headings  []string `json:"headings,omitempty"`
	ID        string   `json:"id"`
}

func loadGolden(t *testing.T, name string) goldenFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", name))
	if err != nil {
		t.Fatalf("reading golden %s: %v", name, err)
	}
	var g goldenFile
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("decoding golden %s: %v", name, err)
	}
	return g
}

func assertGolden(t *testing.T, g goldenFile, got []Chunk) {
	t.Helper()
	if len(got) != len(g.Chunks) {
		t.Fatalf("got %d chunks, golden has %d", len(got), len(g.Chunks))
	}
	for i, want := range g.Chunks {
		c := got[i]
		if c.StartByte != want.StartByte || c.EndByte != want.EndByte || c.Lang != want.Lang || c.ID != want.ID {
			t.Errorf("chunk %d = {start:%d end:%d lang:%q id:%q}, want {start:%d end:%d lang:%q id:%q}",
				i, c.StartByte, c.EndByte, c.Lang, c.ID, want.StartByte, want.EndByte, want.Lang, want.ID)
		}
		if strings.Join(c.Headings, "/") != strings.Join(want.Headings, "/") {
			t.Errorf("chunk %d headings = %v, want %v", i, c.Headings, want.Headings)
		}
	}
}

func TestGoldenMarkdown(t *testing.T) {
	g := loadGolden(t, "md_chunk_basic.json")
	got, err := (&MarkdownChunker{}).Chunk(g.InputPath, []byte(g.Input))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertGolden(t, g, got)
}

func TestGoldenGo(t *testing.T) {
	g := loadGolden(t, "go_chunk_basic.json")
	got, err := (&CodeChunker{lang: "go"}).Chunk(g.InputPath, []byte(g.Input))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertGolden(t, g, got)
}

func TestGoldenOtherLang(t *testing.T) {
	g := loadGolden(t, "other_lang_chunk.json")
	got, err := (&CodeChunker{lang: "rust"}).Chunk(g.InputPath, []byte(g.Input))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertGolden(t, g, got)
}
