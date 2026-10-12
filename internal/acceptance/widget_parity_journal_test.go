package acceptance

// Purpose (this file): the default-lane tests of the widget parity journal
//   verifier. A complete seeded journal passes; each seeded defect, applied
//   alone to a fresh copy, turns the verifier red with a message that names
//   the locator. The live check (widget_parity_journal_live_test.go, tag
//   widgetparity) runs the same verifier over the owner's real journal.
// Inputs: testdata/widget_parity/seed and the journal template.
// Constraints: the tests drive no GUI and read no real machine state; every
//   defect is one edit to a copy, and the edit helpers refuse a vacuous one.
// Contract: contract:widget-parity-journal (P1-WID-10).

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestWidgetParityJournal_Seeded(t *testing.T) {
	res := newSeedTree(t).verify()
	if len(res.problems) != 0 {
		t.Fatalf("a complete seeded journal must pass, got %v", res.problems)
	}
	if n := len(res.passed) + len(res.disclosed) + len(res.deferred); n != len(parityLocators) {
		t.Fatalf("every locator must land in exactly one list: %d of %d", n, len(parityLocators))
	}
	if !slices.Equal(res.disclosed, []string{"C6P"}) {
		t.Errorf("disclosed-not-passed list = %v, want [C6P]", res.disclosed)
	}
	t.Run("rows", func(t *testing.T) { runParityDefects(t, rowDefects()) })
	t.Run("evidence", func(t *testing.T) { runParityDefects(t, evidenceDefects()) })
	t.Run("signing", func(t *testing.T) { runParityDefects(t, signingDefects()) })
	t.Run("fresh-install", func(t *testing.T) { runParityDefects(t, freshInstallDefects()) })
	t.Run("fresh-install at exactly 30 s passes", func(t *testing.T) {
		s := newSeedTree(t)
		s.replaceIn("evidence/fresh-install/widget-status.json", "09:01:12Z", "09:01:30Z")
		if p := s.verify().problems; len(p) != 0 {
			t.Fatalf("30 s is inside the limit, got %v", p)
		}
	})
}

func TestWidgetParityJournal_DeferredP2Rows(t *testing.T) {
	res := newSeedTree(t).verify()
	if len(res.problems) != 0 {
		t.Fatalf("a complete seeded journal with four deferred rows must pass, got %v", res.problems)
	}
	four := []string{"PAR-CO", "PAR-GP", "PAR-OC", "PAR-ZA"}
	if !slices.Equal(res.deferred, four) {
		t.Errorf("deferred-P2 list = %v, want %v", res.deferred, four)
	}
	if !slices.Contains(res.passed, "PAR-A") || len(res.passed) == 0 {
		t.Errorf("passed list %v must hold PAR-A", res.passed)
	}
	for _, l := range four {
		if slices.Contains(res.passed, l) {
			t.Errorf("deferred row %s is in the passed list", l)
		}
	}
	t.Run("status", func(t *testing.T) { runParityDefects(t, statusDefects()) })
	t.Run("feed row", func(t *testing.T) { runParityDefects(t, feedRowDefects()) })
	t.Run("a passed PAR-CO moves lists", func(t *testing.T) {
		s := newSeedTree(t)
		s.setCell("PAR-CO", 1, parityPassed)
		s.write("evidence/par-co-note.txt", "seeded observation: the Codex row now passes\n")
		s.setCell("PAR-CO", 2, "evidence/par-co-note.txt")
		r := s.verify()
		if len(r.problems) != 0 || !slices.Contains(r.passed, "PAR-CO") || slices.Contains(r.deferred, "PAR-CO") {
			t.Fatalf("PAR-CO passed: problems %v, passed %v, deferred %v", r.problems, r.passed, r.deferred)
		}
	})
	t.Run("a failed PAR-CO is in no list", func(t *testing.T) {
		s := newSeedTree(t)
		s.setCell("PAR-CO", 1, parityFailed)
		if r := s.verify(); len(r.problems) == 0 || slices.Contains(r.deferred, "PAR-CO") || slices.Contains(r.passed, "PAR-CO") {
			t.Fatalf("a failed row must be red and listed nowhere: %v / %v / %v", r.problems, r.passed, r.deferred)
		}
	})
}

// TestWidgetParityJournal_AllPassedEmptyEvidence is the fail-closed probe: a
// journal that says passed everywhere but whose evidence files are empty is
// red, and names every locator.
func TestWidgetParityJournal_AllPassedEmptyEvidence(t *testing.T) {
	s := newSeedTree(t)
	for _, l := range parityLocators {
		s.setCell(l, 1, parityPassed)
	}
	s.blankEvidence()
	res := s.verify()
	if len(res.passed) != 0 {
		t.Fatalf("no row may pass over empty evidence, passed %v", res.passed)
	}
	for _, l := range parityLocators {
		if !slices.ContainsFunc(res.problems, func(p string) bool { return strings.HasPrefix(p, parityRowPrefix+l+":") }) {
			t.Errorf("no problem names %s", l)
		}
	}
}

// TestWidgetParityJournal_ArtifactBinding swaps the artifact after the journal
// was written: every row's typed hash is now stale.
func TestWidgetParityJournal_ArtifactBinding(t *testing.T) {
	s := newSeedTree(t)
	s.write(seedArtifact, "a different build\n")
	res := s.verify()
	for _, l := range parityLocators {
		want := parityRowPrefix + l + ": artifact sha256"
		if !slices.ContainsFunc(res.problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("%s is not flagged stale after the artifact changed", l)
		}
	}
	t.Run(".app bundle hashes its executable", func(t *testing.T) {
		s := newSeedTree(t)
		s.write("../Seed.app/Contents/MacOS/"+parityBundleExecutable, "exe\n")
		got, err := artifactSHA256(s.path("../Seed.app"))
		if err != nil || len(got) != 64 {
			t.Fatalf("app hash = %q, %v", got, err)
		}
		if _, err := artifactSHA256(s.path("evidence")); err == nil {
			t.Fatal("a directory that is not a .app must be refused")
		}
		if _, err := artifactSHA256(s.path("nope.dmg")); err == nil {
			t.Fatal("a missing artifact must be refused")
		}
	})
}

var templateSentenceRE = regexp.MustCompile(`(?m)^- WIDGET/([A-Z0-9-]+): (.{20,})$`)

// TestWidgetParityJournal_TemplateMatchesLocators fails when the template and
// the verifier disagree on the locator set, or a locator has no sentence.
func TestWidgetParityJournal_TemplateMatchesLocators(t *testing.T) {
	b, err := os.ReadFile("testdata/widget_parity_journal.template.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	rows, malformed := parseParityRows(text)
	if len(malformed) != 0 {
		t.Fatalf("template rows are malformed: %v", malformed)
	}
	var tableSet []string
	for _, r := range rows {
		tableSet = append(tableSet, r.locator)
	}
	var sentences []string
	said := map[string]string{}
	for _, m := range templateSentenceRE.FindAllStringSubmatch(text, -1) {
		sentences = append(sentences, m[1])
		said[m[1]] = m[2]
	}
	for name, got := range map[string][]string{"table": tableSet, "sentences": sentences} {
		if !slices.Equal(got, parityLocators) {
			t.Errorf("template %s locators = %v, want %v", name, got, parityLocators)
		}
	}
	for _, want := range []string{parityDeferralCite, "DISCLOSED-NOT-PASSED"} {
		if !strings.Contains(said["C6P"], want) {
			t.Errorf("C6P sentence must carry %q: %s", want, said["C6P"])
		}
	}
	if !strings.Contains(text, "ref/widget-v1-parity/INTAKE-2026-09-28.md") || !strings.Contains(text, "§1") {
		t.Error("template must name the widget v1 parity intake §1 as the PAR reference")
	}
}

// TestWidgetParityJournal_TemplateIsRed: the unfilled template must fail
// verification and name every locator.
func TestWidgetParityJournal_TemplateIsRed(t *testing.T) {
	s := newSeedTree(t)
	tmpl, err := os.ReadFile("testdata/widget_parity_journal.template.md")
	if err != nil {
		t.Fatal(err)
	}
	s.write("journal.md", string(tmpl))
	res := s.verify()
	for _, l := range parityLocators {
		if !slices.ContainsFunc(res.problems, func(p string) bool { return strings.HasPrefix(p, parityRowPrefix+l+":") }) {
			t.Errorf("the unfilled template does not fail on %s", l)
		}
	}
}

// TestWidgetParityJournal_SigningNeedsLiveSources: with no way to recompute the
// signature or the Gatekeeper verdict, SIGNING is red however the files read.
func TestWidgetParityJournal_SigningNeedsLiveSources(t *testing.T) {
	s := newSeedTree(t)
	env := s.env()
	env.codesignOf, env.spctlOf = nil, nil
	res := verifyParityJournal(s.path("journal.md"), env)
	for _, want := range []string{"no codesign source", "no spctl source"} {
		if !slices.ContainsFunc(res.problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("no problem says %q; got %v", want, res.problems)
		}
	}
	if slices.Contains(res.passed, "SIGNING") {
		t.Error("SIGNING must not pass without live sources")
	}
}
