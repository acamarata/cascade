package context

import (
	"strings"
	"testing"
)

// Tests for the CC managed-block renderer: what it emits, what it refuses to
// emit, and the fact that a refusal is always visible on the page.

// planted values used to prove nothing sensitive reaches a generated file.
// Each is a canary in the sense internal/secrets established: if the value
// appears in output, the test that planted it has caught a real leak.
const (
	canaryPath   = "/Users/example/Sites/acamarata/secret-project"
	canaryToken  = "sk-CANARYvalue0123456789abcdefghij"
	canaryAssign = "api_key = CANARYassignedvalue123"
)

func TestUnrenderableDetectsWhatMustNotBeCommitted(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"plain prose", "## Style\n\nShort sentences.", ""},
		{"relative path", "## Layout\n\nSee `internal/context/merge.go`.", ""},
		{"word secret without a value", "## Policy\n\nKeep secrets out of the repo.", ""},
		{"home path", "## Local\n\nRun " + canaryPath, "machine-specific path"},
		{"linux home path", "## Local\n\nRun /home/dev/build.sh", "machine-specific path"},
		{"volume path", "## Local\n\nMounted at /Volumes/DATA/repo", "machine-specific path"},
		{"windows user path", "## Local\n\nAt C:\\Users\\dev\\repo", "machine-specific path"},
		{"private key", "## Keys\n\n-----BEGIN RSA PRIVATE KEY-----", "private key block"},
		{"api token", "## Keys\n\nUse " + canaryToken, "credential-shaped token"},
		{"github token", "## Keys\n\nghp_abcdefghijklmnopqrstuvwxyz01", "credential-shaped token"},
		{"assigned secret", "## Keys\n\n" + canaryAssign, "assigned secret value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unrenderable(tc.content); got != tc.want {
				t.Fatalf("unrenderable(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderTierBlockExcludesAndAnnounces is hard requirement 1 in one test:
// a section that cannot be rendered is left out AND named, and its text
// never reaches the page.
func TestRenderTierBlockExcludesAndAnnounces(t *testing.T) {
	sections := []MergedSection{
		{Heading: "Style", Content: "## Style\n\nShort sentences.", Role: TierGCI},
		{Heading: "Local Paths", Content: "## Local Paths\n\nRun " + canaryPath, Role: TierGCI},
	}
	block := renderTierBlock(TierGCI, sections)

	if strings.Contains(block, canaryPath) {
		t.Fatal("the excluded section's text reached the generated block")
	}
	if !strings.Contains(block, `"Local Paths"`) {
		t.Error("the excluded section's heading is not named in the block")
	}
	if !strings.Contains(block, "machine-specific path") {
		t.Error("the block does not say why the section was held back")
	}
	if !strings.Contains(block, "Short sentences.") {
		t.Error("the renderable section was lost alongside the excluded one")
	}
	if !strings.Contains(block, "1 section from this tier could not be rendered") {
		t.Errorf("the notice does not count the exclusion:\n%s", block)
	}
}

// TestRenderTierBlockWhollyUnrenderableTierStillEmits is the case the ticket
// singles out: a tier none of whose content can be rendered still produces a
// block, and that block says what happened. A tier that shrank silently to a
// bare header would be indistinguishable from a tier that went missing.
func TestRenderTierBlockWhollyUnrenderableTierStillEmits(t *testing.T) {
	sections := []MergedSection{
		{Content: "Preamble mentioning " + canaryPath, Role: TierPRI},
		{Heading: "Keys", Content: "## Keys\n\n" + canaryToken, Role: TierPRI},
	}
	block := renderTierBlock(TierPRI, sections)

	for _, canary := range []string{canaryPath, canaryToken} {
		if strings.Contains(block, canary) {
			t.Fatalf("a planted canary reached the generated block")
		}
	}
	if !strings.Contains(block, "2 sections from this tier could not be rendered") {
		t.Errorf("the notice does not report both exclusions:\n%s", block)
	}
	if !strings.Contains(block, preambleLabel) {
		t.Error("the excluded preamble is not named")
	}
	if !strings.Contains(block, `"Keys"`) {
		t.Error("the excluded heading is not named")
	}
	if !strings.Contains(block, "## Cascade Context — PRI Tier") {
		t.Error("the tier header is missing, so the file does not say which tier it belongs to")
	}
}

// TestRenderTierBlockNeverLeaksAnyCanary is the canary sweep over the whole
// rule set: for every detector, a section carrying its trigger must be
// excluded, and the trigger must not appear anywhere in the output.
func TestRenderTierBlockNeverLeaksAnyCanary(t *testing.T) {
	planted := []string{canaryPath, canaryToken, canaryAssign, "-----BEGIN EC PRIVATE KEY-----"}
	for _, p := range planted {
		if unrenderable("## X\n\n"+p) == "" {
			t.Fatalf("planted value %q is not detected at all, so this test proves nothing", p)
		}
		block := renderTierBlock(TierGCI, []MergedSection{{Heading: "X", Content: "## X\n\n" + p}})
		if strings.Contains(block, p) {
			t.Errorf("planted value %q reached the generated block", p)
		}
	}
}

// TestRenderTierBlockDigestMatchesBody pins the invariant the write path
// depends on: the digest in the marker line always describes the body under
// it, so an untouched block never reads as hand-edited.
func TestRenderTierBlockDigestMatchesBody(t *testing.T) {
	block := renderTierBlock(TierASI, []MergedSection{
		{Heading: "A", Content: "## A\n\nbody", Role: TierASI},
	})
	if !managedBlockIntact(block) {
		t.Fatal("a freshly rendered block does not verify against its own digest")
	}
	if !strings.HasPrefix(block, markerOpenPrefix+digestAttr) {
		t.Errorf("block does not open with a digest-carrying marker:\n%s", block)
	}
	if !strings.HasSuffix(block, markerClose) {
		t.Errorf("block does not close with the managed marker:\n%s", block)
	}
}

// TestGroupByRoleIsOrdinalOrdered runs the grouping repeatedly, because the
// defect it guards against (ranging over a map to produce output) shows up
// as an intermittent reordering, not a consistent one.
func TestGroupByRoleIsOrdinalOrdered(t *testing.T) {
	mc := MergedContext{Sections: []MergedSection{
		{Heading: "a", Role: TierGCI, Ordinal: 0},
		{Heading: "b", Role: TierASI, Ordinal: 1},
		{Heading: "c", Role: TierPPI, Ordinal: 2},
		{Heading: "d", Role: TierPRI, Ordinal: 3},
		{Heading: "e", Role: TierPAI, Ordinal: 4},
		{Heading: "f", Role: TierGCI, Ordinal: 0},
	}}
	want := []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}
	for i := 0; i < 50; i++ {
		roles, buckets := groupByRole(mc)
		if len(roles) != len(want) {
			t.Fatalf("run %d: got %d roles, want %d", i, len(roles), len(want))
		}
		for j, r := range roles {
			if r != want[j] {
				t.Fatalf("run %d: role %d = %s, want %s", i, j, r, want[j])
			}
		}
		if len(buckets[TierGCI]) != 2 {
			t.Fatalf("run %d: GCI bucket holds %d sections, want 2", i, len(buckets[TierGCI]))
		}
	}
}

// TestTierHeaderNamesEveryRole guards against a role being added without a
// description, which would render a header reading "( )".
func TestTierHeaderNamesEveryRole(t *testing.T) {
	for _, role := range allTierRoles() {
		h := tierHeader(role)
		if strings.Contains(h, "()") {
			t.Errorf("tier %s has no description in its header: %q", role, h)
		}
		if !strings.Contains(h, role.String()+" Tier") {
			t.Errorf("tier %s header does not name the tier: %q", role, h)
		}
	}
}
