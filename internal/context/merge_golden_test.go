package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden-driven tests for MergeTiers over the v1-harvested tier corpus at
// testdata/v1-goldens/merge/ (provenance in that directory's README.md).
// Split from merge_test.go solely to stay under Art.10.3's 300-line cap.

// goldenTierFiles maps each v2 role to its harvested v1 tier file, BY ROLE
// (never by position — the mapping table is in the testdata README).
var goldenTierFiles = []struct {
	role TierRole
	file string
}{
	{TierGCI, "tier-gci.md"},
	{TierASI, "tier-asi.md"},
	{TierPPI, "tier-ppi.md"},
	{TierPRI, "tier-pri.md"},
	{TierPAI, "tier-pai.md"},
}

// loadGoldenTiers reads the harvested corpus into the []TierRecord shape
// Discover produces: ascending ordinal, one record per role.
func loadGoldenTiers(t *testing.T) []TierRecord {
	t.Helper()
	dir := filepath.Join("testdata", "v1-goldens", "merge")
	records := make([]TierRecord, 0, len(goldenTierFiles))
	for i, tf := range goldenTierFiles {
		path := filepath.Join(dir, tf.file)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading golden tier %s: %v", path, err)
		}
		records = append(records, TierRecord{
			Role: tf.role, Ordinal: i, Dir: dir, Path: path, Content: string(content),
		})
	}
	return records
}

// renderMerge produces the canonical text compared against
// expected-merge.txt: the surviving sections in emission order, then every
// input block that did not survive with the tier that beat it. Both lists
// are built from ordered slices only — Provenance is never ranged over,
// because Go randomizes map iteration and the output must be byte-stable.
func renderMerge(merged MergedContext, tiers []TierRecord) string {
	var b strings.Builder
	b.WriteString("sections\n")
	kept := map[string]TierRole{}
	for _, s := range merged.Sections {
		heading := s.Heading
		if heading == "" {
			heading = "(preamble)"
		} else {
			kept[s.Heading] = s.Role
		}
		fmt.Fprintf(&b, "%d %s %s\n", s.Ordinal, s.Role, heading)
	}
	b.WriteString("\ndropped\n")
	for _, rec := range tiers {
		if rec.Absent {
			continue
		}
		for _, blk := range splitSections(rec.Content) {
			if blk.heading == "" {
				continue
			}
			if winner, ok := kept[blk.heading]; ok && winner != rec.Role {
				fmt.Fprintf(&b, "%d %s %s -> won by %s\n", rec.Ordinal, rec.Role, blk.heading, winner)
			}
		}
	}
	return b.String()
}

// stripGoldenComments removes the golden file's leading "#" commentary and
// its blank separator so the comparison is over data lines only.
func stripGoldenComments(raw string) string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}

// TestMergeGolden asserts byte-for-byte parity between MergeTiers' rendered
// result over the harvested v1 tier corpus and the hand-written,
// spec-derived expectation in expected-merge.txt.
func TestMergeGolden(t *testing.T) {
	tiers := loadGoldenTiers(t)
	merged, err := MergeTiers(tiers)
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "merge", "expected-merge.txt"))
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	want := stripGoldenComments(string(raw))
	got := renderMerge(merged, tiers)
	if got != want {
		t.Errorf("merge golden mismatch\n--- want ---\n%s--- got ---\n%s", want, got)
	}
}

// TestMergeGoldenDeterministic runs the same input twice and requires
// byte-identical renderings. It is the guard against a map ever reaching
// the output path: Go randomizes map iteration order per run AND per range,
// so a regression here shows up as a flake, not a clean failure.
func TestMergeGoldenDeterministic(t *testing.T) {
	tiers := loadGoldenTiers(t)
	first, err := MergeTiers(tiers)
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	want := renderMerge(first, tiers)
	for i := range 32 {
		next, err := MergeTiers(tiers)
		if err != nil {
			t.Fatalf("MergeTiers() error on run %d = %v", i, err)
		}
		if got := renderMerge(next, tiers); got != want {
			t.Fatalf("run %d differed from run 0:\n--- run 0 ---\n%s--- run %d ---\n%s", i, want, i, got)
		}
	}
}

// TestMergeGoldenPreservesV1Invariants checks the merged result against the
// two behaviours v1 PINNED in a test rather than only describing
// (cascade-v1 crates/cascade-core/tests/tier_distinction.rs
// instruction_text_is_merged_most_general_first_and_nothing_is_dropped):
//
//  1. text is emitted most-general-first, GCI before PAI;
//  2. no tier's text is dropped.
//
// v2 keeps (1) exactly. It narrows (2), and the narrowing is the whole point
// of this ticket: v1 concatenated every tier verbatim, so a lower tier
// could contradict a higher one and the reader met both. v2 resolves the
// contradiction, which means a block CAN be dropped — but only when a
// strictly-higher tier defined the same heading. This test states that
// narrowing as an assertion so it can never widen by accident into "some
// content vanished and nobody can say why".
func TestMergeGoldenPreservesV1Invariants(t *testing.T) {
	tiers := loadGoldenTiers(t)
	merged, err := MergeTiers(tiers)
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}

	// (1) most-general-first: ordinals never decrease across the output.
	prev := -1
	for _, s := range merged.Sections {
		if s.Ordinal < prev {
			t.Fatalf("section %q from ordinal %d emitted after ordinal %d — not most-general-first",
				s.Heading, s.Ordinal, prev)
		}
		prev = s.Ordinal
	}

	// Every non-absent tier still contributes something (its preamble at
	// minimum): no tier may disappear from the output wholesale.
	contributed := map[TierRole]bool{}
	for _, s := range merged.Sections {
		contributed[s.Role] = true
	}
	for _, rec := range tiers {
		if !rec.Absent && !contributed[rec.Role] {
			t.Errorf("tier %s contributed no section at all — a whole tier vanished", rec.Role)
		}
	}

	// (2, narrowed) every dropped block has a same-heading winner at a
	// STRICTLY lower ordinal. Nothing is dropped for any other reason.
	for _, rec := range tiers {
		for _, blk := range splitSections(rec.Content) {
			if blk.heading == "" || sectionFrom(merged, blk.heading, rec.Role) {
				continue
			}
			winner, ok := merged.Provenance[blk.heading]
			if !ok {
				t.Errorf("tier %s block %q was dropped with no winner recorded", rec.Role, blk.heading)
				continue
			}
			if winner >= rec.Role {
				t.Errorf("tier %s block %q was dropped in favour of %s, which is not a higher tier",
					rec.Role, blk.heading, winner)
			}
		}
	}
}

// sectionFrom reports whether merged carries the given heading contributed
// by role.
func sectionFrom(merged MergedContext, heading string, role TierRole) bool {
	for _, s := range merged.Sections {
		if s.Heading == heading && s.Role == role {
			return true
		}
	}
	return false
}

// TestMergeGoldenContentIsVerbatim asserts the winning blocks carry the
// harvested file's real bytes: each surviving section's Content must be a
// substring of the tier file it claims to come from, and a heading section
// must start with its own heading line.
func TestMergeGoldenContentIsVerbatim(t *testing.T) {
	tiers := loadGoldenTiers(t)
	merged, err := MergeTiers(tiers)
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	byRole := map[TierRole]string{}
	for _, rec := range tiers {
		byRole[rec.Role] = rec.Content
	}
	for _, s := range merged.Sections {
		if !strings.Contains(byRole[s.Role], s.Content) {
			t.Errorf("section %q claims tier %s but its content is not in that file", s.Heading, s.Role)
		}
		if s.Heading != "" && !strings.HasPrefix(s.Content, "## "+s.Heading) {
			t.Errorf("section %q does not start with its own heading line", s.Heading)
		}
	}
}

// TestMergeGoldenHeadingsAreExactMatches pins the exact-match keying that
// lets "## Master List" (PRI) and "## Master Lists" (PPI) both survive. A
// normalizing key (lowercased, singularized, punctuation-stripped) would
// silently collapse them and delete one repo's real instructions.
func TestMergeGoldenHeadingsAreExactMatches(t *testing.T) {
	merged, err := MergeTiers(loadGoldenTiers(t))
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	for heading, want := range map[string]TierRole{"Master Lists": TierPPI, "Master List": TierPRI} {
		if got, ok := merged.Provenance[heading]; !ok || got != want {
			t.Errorf("Provenance[%q] = %v (present=%t), want %v", heading, got, ok, want)
		}
	}
	headings := 0
	for _, s := range merged.Sections {
		if s.Heading != "" {
			headings++
		}
	}
	if headings != len(merged.Provenance) {
		t.Errorf("emitted %d heading sections but Provenance has %d entries", headings, len(merged.Provenance))
	}
}
