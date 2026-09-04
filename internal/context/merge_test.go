package context

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// specPrecedenceOrder is R-14.15's rule transcribed from the ruling's own
// words — "higher TIER wins — GCI (lowest ordinal, furthest from cwd) wins
// on same-section conflict; lower tiers ADD sections but never override
// higher-tier content" — into the order authority runs in. It is written
// here INDEPENDENTLY of tier.go's iota block and of MergeTiers' behaviour on
// purpose: a precedence test that derives its expectation from the code it
// checks passes just as happily with the order inverted.
var specPrecedenceOrder = []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}

// rec builds a present TierRecord at the given ordinal.
func rec(role TierRole, ordinal int, content string) TierRecord {
	return TierRecord{Role: role, Ordinal: ordinal, Dir: "/x", Path: "/x/.cascade/CASCADE.md", Content: content}
}

// winnerOf returns the role that won heading, and whether it was won.
func winnerOf(t *testing.T, merged MergedContext, heading string) TierRole {
	t.Helper()
	role, ok := merged.Provenance[heading]
	if !ok {
		t.Fatalf("heading %q is absent from the merged result entirely", heading)
	}
	return role
}

// TestMergePrecedenceMatchesSpecOrder walks every ordered PAIR of tiers,
// has both define the same heading with different bodies, and requires the
// one the SPEC puts first to win. Ten pairs, so an inversion anywhere in the
// chain is caught, not just at the ends.
func TestMergePrecedenceMatchesSpecOrder(t *testing.T) {
	for i, high := range specPrecedenceOrder {
		for j := i + 1; j < len(specPrecedenceOrder); j++ {
			low := specPrecedenceOrder[j]
			t.Run(fmt.Sprintf("%s_beats_%s", high, low), func(t *testing.T) {
				merged, err := MergeTiers([]TierRecord{
					rec(high, i, "## Rule\nfrom the higher tier\n"),
					rec(low, j, "## Rule\nfrom the lower tier\n"),
				})
				if err != nil {
					t.Fatalf("MergeTiers() error = %v", err)
				}
				if got := winnerOf(t, merged, "Rule"); got != high {
					t.Errorf("Provenance[Rule] = %s, want %s (spec: the higher tier wins)", got, high)
				}
				if len(merged.Sections) != 1 {
					t.Fatalf("got %d sections, want exactly 1", len(merged.Sections))
				}
				if !strings.Contains(merged.Sections[0].Content, "higher") {
					t.Errorf("surviving body = %q, want the higher tier's", merged.Sections[0].Content)
				}
			})
		}
	}
}

// TestMergeLowerTierAddsButNeverOverrides is the other half of R-14.15: a
// heading only the lower tier defines is ADDED, and it lands after the
// higher tier's sections.
func TestMergeLowerTierAddsButNeverOverrides(t *testing.T) {
	merged, err := MergeTiers([]TierRecord{
		rec(TierGCI, 0, "## Shared\ngci body\n"),
		rec(TierPAI, 4, "## Shared\npai body\n## App Only\napp body\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if got := winnerOf(t, merged, "Shared"); got != TierGCI {
		t.Errorf("Shared won by %s, want GCI", got)
	}
	if got := winnerOf(t, merged, "App Only"); got != TierPAI {
		t.Errorf("App Only won by %s, want PAI", got)
	}
	if len(merged.Sections) != 2 || merged.Sections[0].Heading != "Shared" || merged.Sections[1].Heading != "App Only" {
		t.Fatalf("sections = %+v, want [Shared(GCI), App Only(PAI)] in that order", merged.Sections)
	}
}

// TestMergeEmptySectionDiffersFromAbsentSection pins the deliberate
// difference between a tier that DEFINES a heading with an empty body and
// one that does not mention it at all. Defining it empty is an override to
// nothing and suppresses the lower tier; omitting it lets the lower tier
// supply the section. Collapsing the two would either resurrect content a
// higher tier deleted, or delete content nobody asked to delete.
func TestMergeEmptySectionDiffersFromAbsentSection(t *testing.T) {
	defined, err := MergeTiers([]TierRecord{
		rec(TierGCI, 0, "## Rule\n"),
		rec(TierPRI, 3, "## Rule\nrepo body\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if got := winnerOf(t, defined, "Rule"); got != TierGCI {
		t.Errorf("an empty GCI section must still win the heading; got %s", got)
	}
	if strings.Contains(defined.Sections[0].Content, "repo body") {
		t.Error("the lower tier's body leaked into a section the higher tier defined empty")
	}

	omitted, err := MergeTiers([]TierRecord{
		rec(TierGCI, 0, "## Other\ngci body\n"),
		rec(TierPRI, 3, "## Rule\nrepo body\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if got := winnerOf(t, omitted, "Rule"); got != TierPRI {
		t.Errorf("an unmentioned heading must fall through to the lower tier; got %s", got)
	}
}

// TestMergeWithinRecordDuplicateHeadingLowerPositionWins covers R-14.16's
// within-record rule: a heading repeated inside ONE tier file resolves by
// position, earliest wins, and it is not a cross-tier conflict.
func TestMergeWithinRecordDuplicateHeadingLowerPositionWins(t *testing.T) {
	merged, err := MergeTiers([]TierRecord{
		rec(TierPPI, 2, "## Rule\nfirst occurrence\n## Rule\nsecond occurrence\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if len(merged.Sections) != 1 {
		t.Fatalf("got %d sections, want 1 (the duplicate must collapse)", len(merged.Sections))
	}
	if !strings.Contains(merged.Sections[0].Content, "first occurrence") {
		t.Errorf("surviving body = %q, want the first occurrence", merged.Sections[0].Content)
	}
}

// TestMergePreamblesFromEveryTierSurvive pins the pre-heading grammar
// decision: content before a file's first "## " has no heading, so it is not
// conflict-keyed and every tier's title and front matter survives.
func TestMergePreamblesFromEveryTierSurvive(t *testing.T) {
	merged, err := MergeTiers([]TierRecord{
		rec(TierGCI, 0, "# GCI title\nintro\n## Rule\ngci\n"),
		rec(TierPAI, 4, "# PAI title\nintro\n## Rule\npai\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	var preambles []TierRole
	for _, s := range merged.Sections {
		if s.Heading == "" {
			preambles = append(preambles, s.Role)
		}
	}
	if len(preambles) != 2 || preambles[0] != TierGCI || preambles[1] != TierPAI {
		t.Fatalf("preamble roles = %v, want [GCI PAI]", preambles)
	}
	if _, keyed := merged.Provenance[""]; keyed {
		t.Error(`Provenance must not key the "" preamble — several tiers contribute one`)
	}
}

// TestMergeQuotedHeadingInsideFenceIsNotABoundary covers the malformed /
// adversarial block case: an instruction file that quotes markdown at
// itself. A "## " inside a code fence must stay content, or the section it
// sits in is cut in half and its tail is filed under a heading its author
// never wrote.
func TestMergeQuotedHeadingInsideFenceIsNotABoundary(t *testing.T) {
	merged, err := MergeTiers([]TierRecord{
		rec(TierPRI, 3, "## Real\nbefore\n```md\n## Quoted\nexample\n```\nafter\n"),
	})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if len(merged.Sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(merged.Sections), merged.Sections)
	}
	if _, ok := merged.Provenance["Quoted"]; ok {
		t.Error("a heading quoted inside a code fence was treated as a section boundary")
	}
	if !strings.Contains(merged.Sections[0].Content, "after") {
		t.Error("content after the fence was severed from its section")
	}
}

// TestMergeEmptyAndAllAbsent: both merge to an empty result with no error.
func TestMergeEmptyAndAllAbsent(t *testing.T) {
	cases := map[string][]TierRecord{
		"nil":        nil,
		"empty":      {},
		"all_absent": {{Role: TierGCI, Ordinal: 0, Absent: true}, {Role: TierPRI, Ordinal: 3, Absent: true}},
	}
	for name, tiers := range cases {
		t.Run(name, func(t *testing.T) {
			merged, err := MergeTiers(tiers)
			if err != nil {
				t.Fatalf("MergeTiers() error = %v, want nil", err)
			}
			if len(merged.Sections) != 0 {
				t.Errorf("Sections = %+v, want empty", merged.Sections)
			}
			if merged.Provenance == nil {
				t.Error("Provenance must be non-nil even for an empty merge")
			}
		})
	}
}

// TestMergeSingleTier: one tier merges to exactly its own sections.
func TestMergeSingleTier(t *testing.T) {
	merged, err := MergeTiers([]TierRecord{rec(TierPRI, 3, "## A\na\n## B\nb\n")})
	if err != nil {
		t.Fatalf("MergeTiers() error = %v", err)
	}
	if len(merged.Sections) != 2 || merged.Sections[0].Heading != "A" || merged.Sections[1].Heading != "B" {
		t.Fatalf("sections = %+v, want [A B]", merged.Sections)
	}
}

// TestMergeFailsClosedOnMalformedInput is the most important test here. A
// record this engine cannot honour must fail the WHOLE call. Skipping it
// would leave the caller with a merged context missing a tier the user
// believes is in force, with nothing to point at.
func TestMergeFailsClosedOnMalformedInput(t *testing.T) {
	good := rec(TierGCI, 0, "## Rule\ngci\n")
	cases := map[string][]TierRecord{
		"out_of_order_ordinals": {rec(TierASI, 3, "## A\n"), rec(TierPRI, 1, "## B\n")},
		"repeated_ordinal":      {rec(TierASI, 1, "## A\n"), rec(TierPRI, 1, "## B\n")},
		"negative_ordinal":      {rec(TierGCI, -1, "## A\n")},
		"repeated_role":         {rec(TierPRI, 1, "## A\n"), rec(TierPRI, 2, "## B\n")},
		"zero_role":             {{Role: TierRole(0), Ordinal: 0}},
		"out_of_range_role":     {{Role: TierRole(9), Ordinal: 0}},
		"absent_with_content":   {good, {Role: TierPRI, Ordinal: 3, Absent: true, Content: "## Rule\n"}},
		"invalid_utf8":          {good, rec(TierPRI, 3, "## Rule\n\xff\xfe not text\n")},
		"nul_byte":              {good, rec(TierPRI, 3, "## Rule\nbo\x00dy\n")},
	}
	for name, tiers := range cases {
		t.Run(name, func(t *testing.T) {
			merged, err := MergeTiers(tiers)
			if err == nil {
				t.Fatalf("MergeTiers() = %+v, nil — a malformed tier was accepted silently", merged)
			}
			if !errors.Is(err, cascade.ErrInvalidInput) {
				kind, _ := cascade.KindOf(err)
				t.Errorf("error kind = %v, want KindInvalidInput", kind)
			}
			if len(merged.Sections) != 0 {
				t.Errorf("a failed merge returned %d sections; it must return no partial result", len(merged.Sections))
			}
		})
	}
}

// ExampleMergeTiers shows the higher-tier-wins rule on two tiers that
// disagree about one section and agree to differ about another.
func ExampleMergeTiers() {
	merged, err := MergeTiers([]TierRecord{
		{Role: TierGCI, Ordinal: 0, Content: "## Style\nno em dashes\n"},
		{Role: TierPRI, Ordinal: 3, Content: "## Style\nem dashes are fine\n## Tests\nrun go test\n"},
	})
	if err != nil {
		panic(err)
	}
	for _, s := range merged.Sections {
		fmt.Printf("%s <- %s\n", s.Heading, s.Role)
	}
	// Output:
	// Style <- GCI
	// Tests <- PRI
}
