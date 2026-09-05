package context

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden-driven tests for CCInstructionWriter over the v1-harvested tier
// corpus at testdata/v1-goldens/ (provenance in that directory's README.md).
// The expected outputs were assembled from the hand-written merge manifest
// and the harvested tier files, never from this writer's own output.

// ccGoldenFiles maps each role to its expected CLAUDE.md fixture, BY ROLE.
var ccGoldenFiles = map[TierRole]string{
	TierGCI: "gci.CLAUDE.md",
	TierASI: "asi.CLAUDE.md",
	TierPPI: "ppi.CLAUDE.md",
	TierPRI: "pri.CLAUDE.md",
	TierPAI: "pai.CLAUDE.md",
}

// digestPlaceholder is what the golden files carry in place of the managed
// block's content hash. The hash cannot be hand-written, so the test
// computes it here with the standard library directly rather than calling
// the package's own bodyDigest: a golden that borrows the implementation's
// hash helper would agree with a broken helper.
const digestPlaceholder = "<DIGEST>"

// loadCCGolden reads a golden fixture and resolves its digest placeholder.
func loadCCGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "cc", name))
	if err != nil {
		t.Fatalf("reading CC golden %s: %v", name, err)
	}
	line, body, ok := strings.Cut(string(raw), "\n")
	if !ok {
		t.Fatalf("CC golden %s has no marker line", name)
	}
	if !strings.Contains(line, digestPlaceholder) {
		t.Fatalf("CC golden %s does not carry the %s placeholder", name, digestPlaceholder)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(body, "\n")))
	return strings.Replace(line, digestPlaceholder, hex.EncodeToString(sum[:]), 1) + "\n" + body
}

// mergeGoldenCorpus runs the real T1/T2 pipeline over the harvested tier
// files, which is how the generator's input fixtures are derived.
func mergeGoldenCorpus(t *testing.T) MergedContext {
	t.Helper()
	merged, err := MergeTiers(loadGoldenTiers(t))
	if err != nil {
		t.Fatalf("MergeTiers over the golden corpus: %v", err)
	}
	return merged
}

func TestCCGolden(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != len(ccGoldenFiles) {
		t.Fatalf("got %d files, want %d (one per contributing tier)", len(files), len(ccGoldenFiles))
	}
	for _, f := range files {
		name, ok := ccGoldenFiles[f.Role]
		if !ok {
			t.Fatalf("tier %s has no golden fixture", f.Role)
		}
		if f.Name != ccTargetName {
			t.Errorf("tier %s: Name = %q, want %q", f.Role, f.Name, ccTargetName)
		}
		if want := loadCCGolden(t, name); string(f.Content) != want {
			t.Errorf("tier %s: generated content does not match %s\n--- got ---\n%s\n--- want ---\n%s",
				f.Role, name, string(f.Content), want)
		}
	}
}

// TestCCGoldenOrderIsMostGeneralFirst pins the emission order separately
// from the content, so a reordering that happened to keep every file's bytes
// intact still fails.
func TestCCGoldenOrderIsMostGeneralFirst(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}
	for i, f := range files {
		if f.Role != want[i] {
			t.Fatalf("file %d is tier %s, want %s", i, f.Role, want[i])
		}
	}
}

// TestCCGoldenCarriesCLIFallback pins the one v2 delta from the harvested
// v1 header. An MCP outage must degrade to the CLI path rather than leaving
// the harness with an instruction it cannot act on.
func TestCCGoldenCarriesCLIFallback(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, f := range files {
		body := string(f.Content)
		for _, want := range []string{"cascade recall", "cascade context slice", "cascade.search"} {
			if !strings.Contains(body, want) {
				t.Errorf("tier %s: generated file does not mention %q", f.Role, want)
			}
		}
	}
}

// TestCCGenerateIsDeterministic runs the generator repeatedly over the same
// input and requires byte-identical output every time. Determinism is the
// property the golden test silently depends on, so it is asserted directly
// rather than inferred from one passing run.
func TestCCGenerateIsDeterministic(t *testing.T) {
	merged := mergeGoldenCorpus(t)
	var w CCInstructionWriter
	first, err := w.Generate(merged)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 25; i++ {
		again, err := w.Generate(merged)
		if err != nil {
			t.Fatalf("Generate (run %d): %v", i, err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d produced %d files, first run produced %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j].Role != first[j].Role || string(again[j].Content) != string(first[j].Content) {
				t.Fatalf("run %d file %d drifted from the first run", i, j)
			}
		}
	}
}

// TestCCGenerateEmptyContext pins the two boundary answers: no sections is
// no files and no error, and a hand-built context missing its provenance is
// refused rather than half-rendered.
func TestCCGenerateEmptyContext(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(MergedContext{Provenance: map[string]TierRole{}})
	if err != nil {
		t.Fatalf("empty merge: unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("empty merge produced %d files, want 0", len(files))
	}
}

// TestCCGenerateNilProvenance is the contract's "nil MergedContext" case as
// it exists in a language without nil structs: sections present, provenance
// map absent.
func TestCCGenerateNilProvenance(t *testing.T) {
	mc := MergedContext{Sections: []MergedSection{{Heading: "A", Content: "## A", Role: TierGCI}}}
	files, err := (&CCInstructionWriter{}).Generate(mc)
	if err == nil {
		t.Fatal("a merged context with sections and no provenance must be refused")
	}
	if files != nil {
		t.Errorf("an error return must not also carry files, got %d", len(files))
	}
	assertKindInvalidInput(t, err)
}

// TestCCEveryTierSurvivesGeneration is the lose-nothing check stated against
// the merge rather than against the writer: every tier that contributed a
// section to the merge must own exactly one generated file.
func TestCCEveryTierSurvivesGeneration(t *testing.T) {
	merged := mergeGoldenCorpus(t)
	contributing := map[TierRole]bool{}
	for _, s := range merged.Sections {
		contributing[s.Role] = true
	}
	files, err := (&CCInstructionWriter{}).Generate(merged)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := map[TierRole]int{}
	for _, f := range files {
		seen[f.Role]++
	}
	for role := range contributing {
		if seen[role] != 1 {
			t.Errorf("tier %s contributed sections but owns %d generated files, want 1", role, seen[role])
		}
	}
	for role := range seen {
		if !contributing[role] {
			t.Errorf("tier %s owns a generated file but contributed no sections", role)
		}
	}
}

// TestCCGeneratedContentKeepsEveryHeading proves no heading is lost between
// the merge and the rendered bytes.
func TestCCGeneratedContentKeepsEveryHeading(t *testing.T) {
	merged := mergeGoldenCorpus(t)
	files, err := (&CCInstructionWriter{}).Generate(merged)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	rendered := map[TierRole]string{}
	for _, f := range files {
		rendered[f.Role] = string(f.Content)
	}
	for _, s := range merged.Sections {
		if s.Heading == "" {
			continue
		}
		if !strings.Contains(rendered[s.Role], "## "+s.Heading) {
			t.Errorf("heading %q from tier %s is missing from that tier's generated file", s.Heading, s.Role)
		}
	}
}

// ExampleCCInstructionWriter_Generate renders a two-tier cascade.
func ExampleCCInstructionWriter_Generate() {
	merged, err := MergeTiers([]TierRecord{
		{Role: TierGCI, Ordinal: 0, Content: "## Style\n\nShort sentences.\n"},
		{Role: TierPRI, Ordinal: 3, Content: "## Style\n\nLong sentences.\n\n## Tests\n\nRun them.\n"},
	})
	if err != nil {
		panic(err)
	}
	files, err := (&CCInstructionWriter{}).Generate(merged)
	if err != nil {
		panic(err)
	}
	for _, f := range files {
		fmt.Printf("%s -> %s (%d bytes)\n", f.Role, f.Name, len(f.Content))
	}
	fmt.Println("Style won by:", merged.Provenance["Style"])
	fmt.Println("PRI file mentions Long sentences:",
		strings.Contains(string(files[1].Content), "Long sentences"))
	// Output:
	// GCI -> .claude/CLAUDE.md (556 bytes)
	// PRI -> .claude/CLAUDE.md (543 bytes)
	// Style won by: GCI
	// PRI file mentions Long sentences: false
}
