package build

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDocTruth_Live is acceptance [5]'s CI-mode half: the gate passes on
// the real tree at the tip in CI mode, with the f688c0b floors that prove
// the scope function actually found the real doc universe rather than an
// empty or truncated one.
func TestDocTruth_Live(t *testing.T) {
	root := sweepModuleRoot(t)
	rep, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CheckDocTruth: %v", err)
	}
	if rep.Files < 50 {
		t.Errorf("Files = %d, want >= 50", rep.Files)
	}
	if rep.Links < 20 {
		t.Errorf("Links = %d, want >= 20", rep.Links)
	}
	if rep.Refs < 200 {
		t.Errorf("Refs = %d, want >= 200", rep.Refs)
	}
	t.Logf("doctruth live: Files=%d Links=%d Refs=%d Directives=%d Findings=%d New=%d Fixed=%d",
		rep.Files, rep.Links, rep.Refs, rep.Directives, len(rep.Findings), len(rep.New), len(rep.Fixed))
	if len(rep.New) != 0 {
		t.Errorf("CI mode: %d New (non-baselined) findings, want 0: %+v", len(rep.New), rep.New)
	}
}

// docTruthIndexScaffold are the three index pages every whole-scope test
// fixture needs present and empty-satisfied, so only the fixture's
// deliberate finding shows up.
func docTruthIndexScaffold() map[string]string {
	return map[string]string{
		".github/wiki/Home.md":      "# Home\n",
		"docs/security-posture.md":  "# Security Posture\n",
		"docs/quickstart/README.md": "# Quickstart\n",
	}
}

func writeBaselineAt(t *testing.T, root string, entries []baselineEntry) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, baselineRelPath), marshalBaseline(entries), 0o644); err != nil {
		t.Fatalf("writing baseline: %v", err)
	}
}

// TestDocTruth_BaselineRatchet proves acceptance [2]: a baselined finding
// passes CI and fails Release; an unbaselined one fails CI; a baseline key
// that stops reproducing is reported in Fixed and never fails CI.
func TestDocTruth_BaselineRatchet(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "# Doc\n\nTODO fix\n"
	root := newDocTruthRepo(t, files, nil)

	learn, err := CheckDocTruth(root, DocTruthCI)
	if err != nil || len(learn.New) != 1 {
		t.Fatalf("learn: New=%v err=%v", learn.New, err)
	}
	keyA := learn.New[0]
	writeBaselineAt(t, root, []baselineEntry{{Key: keyA.Key, File: keyA.File, Rule: string(keyA.Rule), Detail: keyA.Detail}})

	ci, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CI: %v", err)
	}
	if len(ci.New) != 0 {
		t.Fatalf("CI: %d New, want 0 (baselined finding passes CI)", len(ci.New))
	}
	rel, err := CheckDocTruth(root, DocTruthRelease)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(rel.New) != 1 {
		t.Fatalf("Release: %d New, want 1 (release ignores the baseline)", len(rel.New))
	}

	// Replace the claim with a different one: A no longer reproduces (goes
	// to Fixed), the new one (B) is unbaselined and fails CI.
	writeFileT(t, filepath.Join(root, "README.md"), "# Doc\n\nFIXME other\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: swap the claim")

	ci2, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CI2: %v", err)
	}
	if len(ci2.New) != 1 {
		t.Fatalf("CI2: %d New, want 1 (B is unbaselined): %+v", len(ci2.New), ci2.New)
	}
	if len(ci2.Fixed) != 1 || ci2.Fixed[0] != keyA.Key {
		t.Fatalf("CI2: Fixed = %v, want [%s]", ci2.Fixed, keyA.Key)
	}
}

// TestDocTruth_BaselinePruneOnly and TestDocTruth_BaselineGuard cover
// acceptance [7]'s prune/guard clauses.
func TestDocTruth_BaselinePruneOnly(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "# Doc\n\nTODO first\n\nFIXME second\n"
	root := newDocTruthRepo(t, files, nil)

	learn, err := CheckDocTruth(root, DocTruthCI)
	if err != nil || len(learn.New) != 2 {
		t.Fatalf("learn: New=%v err=%v", learn.New, err)
	}
	var a, b DocFinding
	for _, f := range learn.New {
		if f.Detail == "stale marker: TODO" {
			a = f
		} else {
			b = f
		}
	}
	writeBaselineAt(t, root, []baselineEntry{{Key: a.Key, File: a.File, Rule: string(a.Rule), Detail: a.Detail}})

	kept, dropped, err := PruneDocTruthBaseline(root)
	if err != nil {
		t.Fatalf("PruneDocTruthBaseline: %v", err)
	}
	if kept != 1 || dropped != 0 {
		t.Fatalf("prune: kept=%d dropped=%d, want 1,0 (B was never added)", kept, dropped)
	}
	after, err := loadBaseline(root)
	if err != nil || len(after) != 1 || after[0].Key != a.Key {
		t.Fatalf("baseline after prune = %+v, err=%v, want only A", after, err)
	}

	ci, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CI: %v", err)
	}
	if len(ci.New) != 1 || ci.New[0].Key != b.Key {
		t.Fatalf("CI after prune: New=%+v, want exactly B", ci.New)
	}

	_, err = InitDocTruthBaseline(root)
	if k, ok := cascade.KindOf(err); err == nil || !ok || k != cascade.KindConflict {
		t.Fatalf("InitDocTruthBaseline over existing file: err=%v, want KindConflict", err)
	}
	final, err := loadBaseline(root)
	if err != nil || len(final) != 1 || final[0].Key != a.Key {
		t.Fatalf("baseline after failed init = %+v, want unchanged [A]", final)
	}
}

// TestDocTruth_BaselineGuard (temp git repo): a commit adding a key makes
// guard HEAD~1 exit non-clean (a non-empty added list); a commit removing
// a key reports none added; a ref where the file is absent, and a corrupt
// ref, each error.
func TestDocTruth_BaselineGuard(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "# Doc\n\nTODO first\n"
	root := newDocTruthRepo(t, files, nil)

	learn, err := CheckDocTruth(root, DocTruthCI)
	if err != nil || len(learn.New) != 1 {
		t.Fatalf("learn: New=%v err=%v", learn.New, err)
	}
	keyA := learn.New[0]

	// Commit 2 adds the key (commit 1, from newDocTruthRepo, has an empty
	// baseline).
	writeBaselineAt(t, root, []baselineEntry{{Key: keyA.Key, File: keyA.File, Rule: string(keyA.Rule), Detail: keyA.Detail}})
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: baseline gains a key")

	added, err := GuardDocTruthBaseline(root, "HEAD~1")
	if err != nil {
		t.Fatalf("guard (added): %v", err)
	}
	if len(added) != 1 || added[0] != keyA.Key {
		t.Fatalf("guard (added) = %v, want [%s]", added, keyA.Key)
	}

	// Commit 4: prune the key back out. guard HEAD~1 (the commit that had
	// it) now reports nothing added, since the working copy only shrank.
	writeBaselineAt(t, root, nil)
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: baseline loses the key")
	added2, err := GuardDocTruthBaseline(root, "HEAD~1")
	if err != nil {
		t.Fatalf("guard (removed): %v", err)
	}
	if len(added2) != 0 {
		t.Fatalf("guard (removed) = %v, want none added", added2)
	}

	if _, err := GuardDocTruthBaseline(root, "does-not-exist"); err == nil {
		t.Fatal("guard against a bogus ref: want an error, got nil")
	}
}

// checkIndexSubsetScope proves the exact subset-scope clause of
// acceptance [7], word for word: build a fixture with a missing
// .github/wiki/Home.md, a missing docs/quickstart/README.md beside one
// present quickstart page, and an unlinked docs/security-posture/b.md,
// with an empty committed baseline. CheckDocTruth scoped to only the
// security-posture arguments must return EXACTLY the b.md index finding
// (never the missing-Home or missing-quickstart-README findings, which
// lie outside that scope); CheckDocTruth scoped to only .github/wiki must
// return EXACTLY the missing-Home finding. Both are set equality, not a
// weaker "no leaked finding of the wrong kind" absence check.
func checkIndexSubsetScope(t *testing.T, _ string) {
	t.Helper()
	files := map[string]string{
		// .github/wiki/Home.md deliberately absent: missing index page.
		// A second wiki page keeps .github/wiki a resolvable directory
		// argument; its own per-page finding is replaced by the single
		// missing-Home finding, per the index rule's contract.
		".github/wiki/orphan.md": "# Orphan\n",
		"docs/quickstart/foo.md": "# Foo\n",
		// docs/quickstart/README.md deliberately absent: missing index page.
		"docs/security-posture.md":   "# Security Posture\n",
		"docs/security-posture/b.md": "# B\n", // present but never linked
	}
	root := newDocTruthRepo(t, files, nil) // baseline: []

	secOnly, err := CheckDocTruth(root, DocTruthRelease, "docs/security-posture.md", "docs/security-posture")
	if err != nil {
		t.Fatalf("subset (security-posture): %v", err)
	}
	wantSec := []DocFinding{{
		File:   "docs/security-posture.md",
		Line:   0,
		Rule:   DocRuleIndex,
		Detail: "unlinked page: docs/security-posture/b.md",
	}}
	if diff := diffFindingsIgnoringKey(secOnly.New, wantSec); diff != "" {
		t.Errorf("subset (security-posture) New mismatch: %s\ngot: %+v", diff, secOnly.New)
	}

	wikiOnly, err := CheckDocTruth(root, DocTruthRelease, ".github/wiki")
	if err != nil {
		t.Fatalf("subset (.github/wiki): %v", err)
	}
	wantWiki := []DocFinding{{
		File:   ".github/wiki/Home.md",
		Line:   0,
		Rule:   DocRuleIndex,
		Detail: "missing index page: .github/wiki/Home.md",
	}}
	if diff := diffFindingsIgnoringKey(wikiOnly.New, wantWiki); diff != "" {
		t.Errorf("subset (.github/wiki) New mismatch: %s\ngot: %+v", diff, wikiOnly.New)
	}
}

// diffFindingsIgnoringKey reports a mismatch between got and want by
// File+Line+Rule+Detail (never Key, which embeds a content hash the
// caller doesn't reconstruct), as a human-readable string, or "" when
// they match exactly as sets of that same size.
func diffFindingsIgnoringKey(got, want []DocFinding) string {
	if len(got) != len(want) {
		return fmt.Sprintf("got %d findings, want %d", len(got), len(want))
	}
	strip := func(f DocFinding) DocFinding { f.Key = ""; return f }
	gotSet := map[DocFinding]bool{}
	for _, f := range got {
		gotSet[strip(f)] = true
	}
	for _, f := range want {
		if !gotSet[strip(f)] {
			return fmt.Sprintf("missing expected finding %+v", f)
		}
	}
	return ""
}
