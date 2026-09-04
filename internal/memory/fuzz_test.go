package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FuzzFrontmatterParse drives the record parser with malformed, truncated
// and adversarial input. The property under test is not merely "no panic":
// every input must either decode to a record that re-encodes to itself, or
// be refused with a taxonomy error, with no third outcome and no partially
// populated record alongside an error.
func FuzzFrontmatterParse(f *testing.F) {
	seedDir := filepath.Join("..", "testdata", "fuzz", "FuzzFrontmatterParse")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		f.Add(data)
	}
	for _, g := range []string{"entry_user.md", "entry_feedback.md", "entry_project.md", "entry_reference.md"} {
		f.Add(mustReadGoldenF(f, g))
	}
	f.Add([]byte(nil))
	f.Add([]byte("---\n"))
	f.Add([]byte("---\n---\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := decodeEntry(data)
		if err != nil {
			if _, ok := cascade.KindOf(err); !ok {
				t.Fatalf("a refusal was not a taxonomy error: %v", err)
			}
			if got != (MemoryEntry{}) {
				t.Fatalf("a refused input produced a populated record: %+v", got)
			}
			return
		}
		again, err := decodeEntry(encodeEntry(got))
		if err != nil {
			t.Fatalf("an accepted record failed to re-decode after encoding: %v", err)
		}
		if again.Name != got.Name || again.Body != got.Body || again.Confidence != got.Confidence {
			t.Fatal("an accepted record did not survive its own round trip")
		}
	})
}

// mustReadGoldenF is mustReadGolden for a *testing.F.
func mustReadGoldenF(f *testing.F, name string) []byte {
	f.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if err != nil {
		f.Fatalf("reading golden %s: %v", name, err)
	}
	return data
}
