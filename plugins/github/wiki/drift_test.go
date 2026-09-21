// Purpose (this file): CheckDrift's contract, including the Art.2
//
//	provenance-stamped fixture: a real GitHub wiki git bundle
//	(testdata/fixtures/hijri-core-wiki.bundle), cloned with the real git
//	binary and diffed against a known-different local directory.
//
// SPORT: plugins/github/wiki:drift (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func wantDriftURL(owner, repo string) string {
	return resolveWikiURL(owner, repo)
}

func driftOpts(t *testing.T, remote, local string) DriftOptions {
	want := wantDriftURL("acamarata", "cascade")
	return DriftOptions{
		Owner: "acamarata", Repo: "cascade",
		LocalDir:  local,
		Runner:    &redirectingRunner{want: want, local: remote},
		MkdirTemp: mkdirTempFor(t),
	}
}

// TestDrift_CleanWhenIdentical: "exits zero and reports 'no drift' when
// identical."
func TestDrift_CleanWhenIdentical(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "same content"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "same content"})

	res, err := CheckDrift(context.Background(), driftOpts(t, remote, local))
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if !res.Clean {
		t.Fatalf("res = %+v, want Clean", res)
	}
	if DriftExitCode(res) != 0 {
		t.Fatalf("DriftExitCode(clean) = %d, want 0", DriftExitCode(res))
	}
}

// TestDrift_ReportsAddedRemovedChanged: "exits non-zero and reports
// per-file drift when local differs from remote."
func TestDrift_ReportsAddedRemovedChanged(t *testing.T) {
	remote := newBareRemote(t)
	seedRemote(t, remote, map[string]string{"Home.md": "remote version", "OnlyRemote.md": "x"})
	local := writeLocalWiki(t, map[string]string{"Home.md": "local version", "OnlyLocal.md": "y"})

	res, err := CheckDrift(context.Background(), driftOpts(t, remote, local))
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if res.Clean {
		t.Fatal("res.Clean = true, want drift reported")
	}
	if DriftExitCode(res) == 0 {
		t.Fatal("DriftExitCode(dirty) = 0, want non-zero")
	}
	if !equalStrings(res.Added, []string{"OnlyLocal.md"}) {
		t.Errorf("Added = %v", res.Added)
	}
	if !equalStrings(res.Removed, []string{"OnlyRemote.md"}) {
		t.Errorf("Removed = %v", res.Removed)
	}
	if !equalStrings(res.Changed, []string{"Home.md"}) {
		t.Errorf("Changed = %v", res.Changed)
	}
}

// TestDrift_GitAbsent: "git-absent behavior (sync and check both) produces
// an actionable error rather than a panic."
func TestDrift_GitAbsent(t *testing.T) {
	old := activeLocator
	activeLocator = fakeLocator{}
	defer func() { activeLocator = old }()

	remote := newBareRemote(t)
	local := writeLocalWiki(t, nil)
	_, err := CheckDrift(context.Background(), driftOpts(t, remote, local))
	if err == nil {
		t.Fatal("CheckDrift with no git binary returned nil error")
	}
	if !strings.Contains(err.Error(), "git binary is not on PATH") {
		t.Fatalf("err = %v, want the git-absent message", err)
	}
}

// TestDrift_CloneFailure points at a remote that does not exist.
func TestDrift_CloneFailure(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist.git")
	local := writeLocalWiki(t, nil)
	opts := driftOpts(t, nonexistent, local)
	_, err := CheckDrift(context.Background(), opts)
	if err == nil {
		t.Fatal("CheckDrift against a nonexistent remote returned nil error")
	}
}

// TestDrift_EmptyRemoteWiki clones a bare repository with zero commits —
// a wiki that was enabled but never given a page. The clone itself
// succeeds; the comparison then reports every local file as "added".
func TestDrift_EmptyRemoteWiki(t *testing.T) {
	remote := newBareRemote(t) // no seedRemote call: zero commits
	local := writeLocalWiki(t, map[string]string{"Home.md": "content"})

	res, err := CheckDrift(context.Background(), driftOpts(t, remote, local))
	if err != nil {
		t.Fatalf("CheckDrift against an empty remote wiki: %v", err)
	}
	if res.Clean {
		t.Fatal("res.Clean = true, want drift (local has a page the empty remote does not)")
	}
	if !equalStrings(res.Added, []string{"Home.md"}) {
		t.Errorf("Added = %v, want [Home.md]", res.Added)
	}
}

// TestDrift_EmptyRemoteAndEmptyLocalIsClean is the degenerate case: an
// empty remote wiki and an empty local directory report no drift.
func TestDrift_EmptyRemoteAndEmptyLocalIsClean(t *testing.T) {
	remote := newBareRemote(t)
	local := writeLocalWiki(t, nil)
	res, err := CheckDrift(context.Background(), driftOpts(t, remote, local))
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if !res.Clean {
		t.Fatalf("res = %+v, want Clean", res)
	}
}

// TestDrift_RealProvenanceStampedBundleFixture is the Art.2 proof: a git
// bundle captured from a REAL GitHub wiki (acamarata/hijri-core,
// testdata/README.md records tool/version/date/source) is cloned with the
// real git binary — never a self-authored dialect — and diffed against a
// local directory this test deliberately makes different from it.
func TestDrift_RealProvenanceStampedBundleFixture(t *testing.T) {
	bundle := filepath.Join("testdata", "fixtures", "hijri-core-wiki.bundle")

	// A local wiki that is KNOWN-DIFFERENT from the bundle's real content:
	// Home.md exists in the bundle with different text (changed), Removed.md
	// exists only here (added relative to the bundle), and the bundle's
	// real API-Reference.md is intentionally absent here (removed relative
	// to the bundle).
	local := writeLocalWiki(t, map[string]string{
		"Home.md":    "a locally-edited Home page, deliberately different from the captured wiki",
		"Removed.md": "present locally only",
	})

	opts := DriftOptions{
		Owner: "acamarata", Repo: "hijri-core",
		LocalDir: local,
		Runner:   &redirectingRunner{want: mustWant(t, "acamarata", "hijri-core"), local: bundle},
	}
	res, err := CheckDrift(context.Background(), opts)
	if err != nil {
		t.Fatalf("CheckDrift against the real bundle fixture: %v", err)
	}
	if res.Clean {
		t.Fatal("res.Clean = true, want drift against the real bundle content")
	}
	if !containsString(res.Changed, "Home.md") {
		t.Errorf("Changed = %v, want it to include Home.md", res.Changed)
	}
	if !containsString(res.Added, "Removed.md") {
		t.Errorf("Added = %v, want it to include Removed.md", res.Added)
	}
	if !containsString(res.Removed, "API-Reference.md") {
		t.Errorf("Removed = %v, want it to include API-Reference.md (real bundle content this local dir omits)", res.Removed)
	}
	// The bundle carries 31 real pages (testdata/README.md); a fixture that
	// silently shrank to near-nothing would still pass the assertions
	// above, so pin the real count too.
	if got := len(res.Removed); got < 25 {
		t.Errorf("Removed has %d entries, want most of the bundle's ~31 real pages (fixture may have been swapped for a dialect)", got)
	}
}

func mustWant(t *testing.T, owner, repo string) string {
	t.Helper()
	return resolveWikiURL(owner, repo)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
