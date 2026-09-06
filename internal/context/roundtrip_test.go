package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The full-pipeline acceptance test for Epic E: a canonical multi-tier
// fixture (testdata/roundtrip/tiers/) driven through MergeTiers and all
// three registered HarnessGenerators, checked against real-harness
// reference captures rather than against the generator's own output.
// Fixture provenance is recorded in testdata/README.md, per Art.2. The one
// internal-only property tested directly, because no external harness
// governs it, is the managed-block splice: a hand edit around the block
// must survive a write byte-for-byte.

// roundtripTierFiles pins the fixture's tier set and ordinals. Ordinals
// only need to be strictly increasing (merge.go's validateTiers), so PPI's
// gap is intentional: the fixture has no PPI stub, matching the codex
// capture's own reach (global, git root, one app level down).
var roundtripTierFiles = []struct {
	role TierRole
	name string
	ord  int
}{
	{TierGCI, "gci.md", 0},
	{TierASI, "asi.md", 1},
	{TierPRI, "pri.md", 3},
	{TierPAI, "pai.md", 4},
}

func loadRoundtripTiers(t *testing.T) []TierRecord {
	t.Helper()
	dir := filepath.Join("testdata", "roundtrip", "tiers")
	recs := make([]TierRecord, 0, len(roundtripTierFiles))
	for _, f := range roundtripTierFiles {
		b, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			t.Fatalf("reading tier fixture %s: %v", f.name, err)
		}
		recs = append(recs, TierRecord{Role: f.role, Ordinal: f.ord, Dir: dir, Path: f.name, Content: string(b)})
	}
	return recs
}

func roundtripMerged(t *testing.T) MergedContext {
	t.Helper()
	mc, err := MergeTiers(loadRoundtripTiers(t))
	if err != nil {
		t.Fatalf("MergeTiers over the roundtrip fixture: %v", err)
	}
	return mc
}

func readReference(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata", "roundtrip"}, parts...)...))
	if err != nil {
		t.Fatalf("reading reference capture %v: %v", parts, err)
	}
	return string(b)
}

// roundtripWriters names the three registered writers for this suite,
// independent of gen_harness_conformance_test.go's own registry helper so
// this acceptance file stands on its own.
func roundtripWriters() map[string]HarnessGenerator {
	return map[string]HarnessGenerator{
		"cx": &CXInstructionWriter{},
		"oc": &OCInstructionWriter{},
		"cc": &CCInstructionWriter{},
	}
}

// TestRoundTripGeneratesAllTiersInOrder pins the ordinal-ordering invariant:
// every writer renders exactly the fixture's four tiers, most general
// first, for every registered writer.
func TestRoundTripGeneratesAllTiersInOrder(t *testing.T) {
	want := []TierRole{TierGCI, TierASI, TierPRI, TierPAI}
	mc := roundtripMerged(t)
	for id, w := range roundtripWriters() {
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", id, err)
		}
		if len(files) != len(want) {
			t.Fatalf("%s: got %d files, want %d", id, len(files), len(want))
		}
		for i, f := range files {
			if f.Role != want[i] {
				t.Errorf("%s: file %d is tier %s, want %s", id, i, f.Role, want[i])
			}
		}
	}
}

// TestRoundTripCarriesCLIFallbackDirective pins R-16.43 (every tier block
// carries the CLI-fallback line) and, in the same pass, that no emitted
// block leaks cascade's own bookkeeping vocabulary: a user reading the file
// should see instructions, not the pipeline that produced them.
func TestRoundTripCarriesCLIFallbackDirective(t *testing.T) {
	forbidden := []string{"TierRecord", "MergedContext", "Provenance", "Ordinal:"}
	mc := roundtripMerged(t)
	for id, w := range roundtripWriters() {
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", id, err)
		}
		for _, f := range files {
			body := string(f.Content)
			if !strings.Contains(body, cliFallbackLine) {
				t.Errorf("%s: tier %s is missing the R-16.43 CLI-fallback directive", id, f.Role)
			}
			for _, term := range forbidden {
				if strings.Contains(body, term) {
					t.Errorf("%s: tier %s leaks internal term %q", id, f.Role, term)
				}
			}
		}
	}
}

// TestRoundTripByteStable requires 25 repeated renders of the same
// MergedContext to be byte-identical, for every writer.
func TestRoundTripByteStable(t *testing.T) {
	const renders = 25
	mc := roundtripMerged(t)
	for id, w := range roundtripWriters() {
		first, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", id, err)
		}
		for i := 1; i < renders; i++ {
			again, err := w.Generate(mc)
			if err != nil {
				t.Fatalf("%s: Generate (render %d): %v", id, i, err)
			}
			for j := range again {
				if string(again[j].Content) != string(first[j].Content) {
					t.Fatalf("%s: render %d tier %s differs from the first render", id, i, again[j].Role)
				}
			}
		}
	}
}

// TestRoundTripMatchesClaudeReference checks the CC writer's PRI-tier
// output against testdata/roundtrip/claude/CLAUDE.md. See testdata/README.md
// for this reference's provenance.
func TestRoundTripMatchesClaudeReference(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(roundtripMerged(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := readReference(t, "claude", "CLAUDE.md")
	if got := priContent(t, files); got != want {
		t.Errorf("CC PRI tier does not match the claude reference capture")
	}
}

// TestRoundTripMatchesOpencodeReference checks the OC writer's PRI-tier
// output against testdata/roundtrip/opencode/AGENTS.md. See
// testdata/README.md for this reference's provenance.
func TestRoundTripMatchesOpencodeReference(t *testing.T) {
	files, err := (&OCInstructionWriter{}).Generate(roundtripMerged(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := readReference(t, "opencode", "AGENTS.md")
	if got := priContent(t, files); got != want {
		t.Errorf("OC PRI tier does not match the opencode reference capture")
	}
}

// TestRoundTripMatchesRealCodexCapture is the strongest evidence in this
// suite: testdata/roundtrip/codex/AGENTS.md is a real `codex debug
// prompt-input` capture fed this package's own CX writer output
// (provenance in testdata/README.md). It asserts the real tool's envelope
// still contains the freshly generated GCI, PRI and PAI blocks verbatim, in
// most-general-first order, that ASI is out of the real tool's reach, and
// that hand-edited prose placed around the PRI managed block survived the
// real tool's own ingestion untouched.
func TestRoundTripMatchesRealCodexCapture(t *testing.T) {
	files, err := (&CXInstructionWriter{}).Generate(roundtripMerged(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	envelope := readReference(t, "codex", "AGENTS.md")
	byRole := map[TierRole]string{}
	for _, f := range files {
		byRole[f.Role] = string(f.Content)
	}
	for _, role := range []TierRole{TierGCI, TierPRI, TierPAI} {
		if !strings.Contains(envelope, byRole[role]) {
			t.Errorf("real codex capture is missing the freshly generated %s block verbatim", role)
		}
	}
	if strings.Contains(envelope, "ASI-ROUNDTRIP") {
		t.Error("real codex capture contains the ASI marker; ASI is above git root and out of codex's reach")
	}
	gciAt := strings.Index(envelope, byRole[TierGCI])
	priAt := strings.Index(envelope, byRole[TierPRI])
	paiAt := strings.Index(envelope, byRole[TierPAI])
	if gciAt < 0 || gciAt >= priAt || priAt >= paiAt {
		t.Error("real codex capture does not order GCI before PRI before PAI")
	}
	if !strings.Contains(envelope, "Maintainer notes (kept above the managed block)") ||
		!strings.Contains(envelope, "Maintainer notes (kept below the managed block).") {
		t.Error("real codex capture lost the hand-edited prose around the PRI managed block")
	}
}

// priContent returns files' PRI-tier content as a string.
func priContent(t *testing.T, files []HarnessFile) string {
	t.Helper()
	return string(mustRole(t, files, TierPRI).Content)
}

// TestRoundTripHandEditSurvivesBeforeAndAfter covers a splice shape the
// existing conformance suite does not: a managed block flanked by
// hand-written prose on BOTH sides. The prefix and suffix must come back
// byte-for-byte.
func TestRoundTripHandEditSurvivesBeforeAndAfter(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(roundtripMerged(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	f := mustRole(t, files, TierPRI)
	const prefix = "# Maintainer notes above\n\nkeep this exact line.\n\n"
	const suffix = "\n\nMaintainer notes below. keep this exact line too.\n"
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(path, []byte(prefix+string(f.Content)+suffix), 0o600); err != nil {
		t.Fatalf("planting the flanked file: %v", err)
	}
	res, err := WriteHarnessFile(path, f, RefuseIfEdited)
	if err != nil {
		t.Fatalf("write over an intact flanked block: %v", err)
	}
	if res.Action != ActionUnchanged {
		t.Fatalf("action = %v, want ActionUnchanged (the block was not edited)", res.Action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !strings.HasPrefix(string(got), prefix) {
		t.Error("the hand-written prefix did not survive byte-for-byte")
	}
	if !strings.HasSuffix(string(got), suffix) {
		t.Error("the hand-written suffix did not survive byte-for-byte")
	}
}

// TestRoundTripHandEditSurvivesWhenBlockAbsent covers the third splice
// shape: hand-written prose with no managed block. The block is appended,
// never replacing the prose.
func TestRoundTripHandEditSurvivesWhenBlockAbsent(t *testing.T) {
	files, err := (&CCInstructionWriter{}).Generate(roundtripMerged(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	f := mustRole(t, files, TierPRI)
	const prose = "# This repo's own CLAUDE.md\n\nWritten by a maintainer, no cascade block yet.\n"
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(path, []byte(prose), 0o600); err != nil {
		t.Fatalf("planting the hand-authored file: %v", err)
	}
	res, err := WriteHarnessFile(path, f, RefuseIfEdited)
	if err != nil {
		t.Fatalf("write onto a file with no managed block: %v", err)
	}
	if res.Action != ActionAppended {
		t.Fatalf("action = %v, want ActionAppended", res.Action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !strings.HasPrefix(string(got), prose) {
		t.Error("the pre-existing prose was not kept verbatim ahead of the appended block")
	}
	if !strings.Contains(string(got), string(f.Content)) {
		t.Error("the generated block was not appended")
	}
}

func mustRole(t *testing.T, files []HarnessFile, role TierRole) HarnessFile {
	t.Helper()
	for _, f := range files {
		if f.Role == role {
			return f
		}
	}
	t.Fatalf("no %s tier in Generate's output", role)
	return HarnessFile{}
}
