package build

import (
	"os"
	"path/filepath"
	"testing"
)

// newDocTruthRepo materializes files (repo-relative path -> content) as a
// fresh committed git repo and returns its root. baseline (may be nil,
// meaning "[]") is written at the fixed baseline path first.
func newDocTruthRepo(t *testing.T, files map[string]string, baseline []baselineEntry) string {
	t.Helper()
	dst := t.TempDir()
	for rel, content := range files {
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeFileT(t, target, content)
	}
	writeFileT(t, filepath.Join(dst, baselineRelPath), string(marshalBaseline(baseline)))
	runGit(t, dst, "init", "-q", "-b", "main")
	runGit(t, dst, "config", "user.email", "fixture@example.invalid")
	runGit(t, dst, "config", "user.name", "Fixture")
	runGit(t, dst, "add", "-A")
	runGit(t, dst, "commit", "-q", "-m", "chore: seed doctruth repo")
	return dst
}

// TestDocTruth_AnchorSlugs covers backticks, punctuation, non-ASCII
// letters, and duplicate-heading suffixing (acceptance [3]).
func TestDocTruth_AnchorSlugs(t *testing.T) {
	content := "# Hello, World!\n\n## `Code` Heading\n\n## Ünïcödé Térms\n\n## Dup\n\n## Dup\n\n## Dup\n"
	hs := parseHeadings(content)
	want := []string{"hello-world", "code-heading", "ünïcödé-térms", "dup", "dup-1", "dup-2"}
	if len(hs) != len(want) {
		t.Fatalf("parseHeadings: got %d headings, want %d: %+v", len(hs), len(want), hs)
	}
	for i, h := range hs {
		if h.slug != want[i] {
			t.Errorf("heading %d: slug = %q, want %q", i, h.slug, want[i])
		}
	}
}

// TestResolveDocLinks_RepoAndWikiURLs covers acceptance [3]'s URL-mapping
// clause: bare wiki page names, .md-suffixed wiki names, and the
// blob/tree/wiki GitHub URL forms all map to tracked repo-relative paths,
// while a non-repo host is reported External and never resolved.
func TestResolveDocLinks_RepoAndWikiURLs(t *testing.T) {
	content := "" +
		"[a](Page)\n" +
		"[b](Page.md)\n" +
		"[c](https://github.com/acamarata/cascade/wiki/Page)\n" +
		"[d](https://github.com/acamarata/cascade/blob/main/docs/x.md)\n" +
		"[e](https://github.com/acamarata/cascade/tree/main/docs/)\n" +
		"[f](https://example.com/other)\n"
	root := newDocTruthRepo(t, map[string]string{
		".github/wiki/Home.md": content,
	}, nil)
	links, _, err := ResolveDocLinks(root, ".github/wiki/Home.md")
	if err != nil {
		t.Fatalf("ResolveDocLinks: %v", err)
	}
	want := []struct {
		target   string
		external bool
	}{
		{".github/wiki/Page.md", false},
		{".github/wiki/Page.md", false},
		{".github/wiki/Page.md", false},
		{"docs/x.md", false},
		{"docs", false},
		{"https://example.com/other", true},
	}
	if len(links) != len(want) {
		t.Fatalf("got %d links, want %d: %+v", len(links), len(want), links)
	}
	for i, l := range want {
		if links[i].Target != l.target || links[i].External != l.external {
			t.Errorf("link %d: Target=%q External=%v, want Target=%q External=%v",
				i, links[i].Target, links[i].External, l.target, l.external)
		}
	}
}

// TestDocTruth_DirectiveScope proves acceptance [4]: the illustrative
// directive silences a path finding on its own line only, never a link or
// claim finding on that line, and a directive on a clean line is itself a
// DocRuleDirective finding.
func TestDocTruth_DirectiveScope(t *testing.T) {
	content := "# Doc\n\n" +
		"[bad](missing.md) `internal/missing-dir/thing.go` <!-- doctruth:illustrative -->\n\n" +
		"TODO more work <!-- doctruth:illustrative -->\n\n" +
		"nothing to suppress here <!-- doctruth:illustrative -->\n"
	root := newDocTruthRepo(t, map[string]string{"README.md": content}, nil)
	rep, err := CheckDocTruth(root, DocTruthRelease)
	if err != nil {
		t.Fatalf("CheckDocTruth: %v", err)
	}
	rules := map[DocRule]int{}
	for _, f := range rep.New {
		rules[f.Rule]++
	}
	if rules[DocRulePath] != 0 {
		t.Errorf("path findings = %d, want 0 (suppressed by directive)", rules[DocRulePath])
	}
	if rules[DocRuleLink] != 1 {
		t.Errorf("link findings = %d, want 1 (directive never suppresses link)", rules[DocRuleLink])
	}
	if rules[DocRuleClaim] != 1 {
		t.Errorf("claim findings = %d, want 1 (directive never suppresses claim)", rules[DocRuleClaim])
	}
	// Line 3's directive suppresses its own path finding (0 path findings,
	// asserted above). Line 5's directive sits beside a CLAIM finding only
	// (never suppressed by the directive), so it counts as "no path/line/
	// symbol/test finding to suppress" and is itself a finding, same as
	// line 7's directive on an otherwise clean line.
	if rules[DocRuleDirective] != 2 {
		t.Errorf("directive findings = %d, want 2 (lines 5 and 7)", rules[DocRuleDirective])
	}
}

// TestDocTruth_UnknownModeIsRelease proves any mode value other than
// DocTruthCI behaves exactly like DocTruthRelease: a mutation that mapped
// the default branch to CI instead would turn this red.
func TestDocTruth_UnknownModeIsRelease(t *testing.T) {
	content := "# Doc\n\nTODO fix\n"
	root := newDocTruthRepo(t, map[string]string{
		"README.md":                 content,
		".github/wiki/Home.md":      "# Home\n",
		"docs/security-posture.md":  "# Security Posture\n",
		"docs/quickstart/README.md": "# Quickstart\n",
	}, nil)

	rep, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CheckDocTruth (learn key): %v", err)
	}
	if len(rep.New) != 1 {
		t.Fatalf("got %d findings, want exactly 1 to baseline: %+v", len(rep.New), rep.New)
	}
	key := rep.New[0].Key

	if err := os.WriteFile(filepath.Join(root, baselineRelPath),
		marshalBaseline([]baselineEntry{{Key: key, File: rep.New[0].File, Rule: string(rep.New[0].Rule), Detail: rep.New[0].Detail}}), 0o644); err != nil {
		t.Fatalf("writing baseline: %v", err)
	}

	repCI, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CheckDocTruth CI: %v", err)
	}
	if len(repCI.New) != 0 {
		t.Fatalf("CI mode: %d New findings, want 0 (baselined)", len(repCI.New))
	}

	repUnknown, err := CheckDocTruth(root, DocTruthMode(7))
	if err != nil {
		t.Fatalf("CheckDocTruth(7): %v", err)
	}
	if len(repUnknown.New) != 1 {
		t.Fatalf("DocTruthMode(7): %d New findings, want 1 (release-strict, baseline ignored)", len(repUnknown.New))
	}
}

// TestDocTruth_DuplicateLineCounted proves acceptance [7]: a baseline
// holding the key of the FIRST of two byte-identical offending lines in
// one file fails CI on the SECOND (a distinct occurrence-2 key).
func TestDocTruth_DuplicateLineCounted(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "# Doc\n\nTODO duplicate\n\nsome text between\n\nTODO duplicate\n"
	root := newDocTruthRepo(t, files, nil)

	learn, err := CheckDocTruth(root, DocTruthCI)
	if err != nil || len(learn.New) != 2 {
		t.Fatalf("learn: New=%v err=%v", learn.New, err)
	}
	first, second := learn.New[0], learn.New[1]
	if first.Line > second.Line {
		first, second = second, first
	}
	if first.Key == second.Key {
		t.Fatalf("duplicate lines produced the same key: %q", first.Key)
	}
	writeBaselineAt(t, root, []baselineEntry{{Key: first.Key, File: first.File, Rule: string(first.Rule), Detail: first.Detail}})

	ci, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CI: %v", err)
	}
	if len(ci.New) != 1 || ci.New[0].Key != second.Key {
		t.Fatalf("CI after baselining the first occurrence: New=%+v, want exactly the second occurrence", ci.New)
	}
}

// TestDocTruth_IndexKeysDistinct proves acceptance [7]'s index clause: two
// unlinked pages get distinct keys; linking one and orphaning a new page
// produces New=={new} and Fixed=={now-linked}. It also proves the subset-
// scope clause: a CheckDocTruth call with explicit file/dir arguments only
// ever reports index findings for the index(es) those arguments cover.
func TestDocTruth_IndexKeysDistinct(t *testing.T) {
	files := docTruthIndexScaffold()
	files[".github/wiki/first.md"] = "# First\n"
	files[".github/wiki/second.md"] = "# Second\n"
	files["docs/security-posture.md"] = "# Security Posture\n\n[A](security-posture/a.md)\n"
	files["docs/security-posture/a.md"] = "# A\n"
	root := newDocTruthRepo(t, files, nil)

	learn, err := CheckDocTruth(root, DocTruthCI)
	if err != nil || len(learn.New) != 2 {
		t.Fatalf("learn: New=%v err=%v", learn.New, err)
	}
	var first, second DocFinding
	for _, f := range learn.New {
		switch f.Detail {
		case "unlinked page: .github/wiki/first.md":
			first = f
		case "unlinked page: .github/wiki/second.md":
			second = f
		}
	}
	if first.Key == "" || second.Key == "" || first.Key == second.Key {
		t.Fatalf("expected two distinct index keys, got first=%+v second=%+v", first, second)
	}
	writeBaselineAt(t, root, []baselineEntry{
		{Key: first.Key, File: first.File, Rule: string(first.Rule), Detail: first.Detail},
		{Key: second.Key, File: second.File, Rule: string(second.Rule), Detail: second.Detail},
	})

	writeFileT(t, filepath.Join(root, ".github/wiki/Home.md"), "# Home\n\n[First](first.md)\n")
	writeFileT(t, filepath.Join(root, ".github/wiki/third.md"), "# Third\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: link first, add unlinked third")

	ci, err := CheckDocTruth(root, DocTruthCI)
	if err != nil {
		t.Fatalf("CI: %v", err)
	}
	if len(ci.New) != 1 || ci.New[0].Detail != "unlinked page: .github/wiki/third.md" {
		t.Fatalf("New = %+v, want exactly the third page", ci.New)
	}
	if len(ci.Fixed) != 1 || ci.Fixed[0] != first.Key {
		t.Fatalf("Fixed = %v, want [%s] (first is now linked)", ci.Fixed, first.Key)
	}

	checkIndexSubsetScope(t, root)
}
