package main

// Purpose: run's two branches (write the manifest; refuse when doing so
// would orphan a non-grandfathered allow entry) against synthetic fixture
// roots, never the real .claude/ tree (gitignored, and this package must
// pass in a CI checkout that has none of it). This is also the mutation
// proof for DEFECT-orphaned-allow-entries-systemic.md's gate 3: the same
// fixture entry is RED while ungrandfathered and GREEN once grandfathered,
// with nothing else changed between the two subtests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// writeGenFixture builds a minimal root with the three inputs run() reads:
// a journals directory, an allow list, and a grandfather list.
func writeGenFixture(t *testing.T, journalNames []string, allowJSON, grandfather string) string {
	t.Helper()
	root := t.TempDir()
	journalsDir := filepath.Join(root, ".claude", "planning", "p1", "phase", "journals")
	if err := os.MkdirAll(journalsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll journals: %v", err)
	}
	for _, name := range journalNames {
		if err := os.WriteFile(filepath.Join(journalsDir, name), []byte("done\n"), 0o644); err != nil {
			t.Fatalf("writing journal %s: %v", name, err)
		}
	}
	buildDir := filepath.Join(root, "internal", "build")
	if err := os.MkdirAll(filepath.Join(buildDir, "testdata"), 0o755); err != nil {
		t.Fatalf("MkdirAll internal/build/testdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "testonly-allow.json"), []byte(allowJSON), 0o644); err != nil {
		t.Fatalf("writing testonly-allow.json: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(buildDir, "testdata", "testonly-orphan-grandfather.txt"), []byte(grandfather), 0o644,
	); err != nil {
		t.Fatalf("writing grandfather list: %v", err)
	}
	return root
}

func testOutputWriter() *output.Writer {
	return output.NewDefault(false, true, false, true)
}

// TestRun_WritesManifestFromRealJournalsOnly proves the filename filter:
// only a bare "<ticket>.md" journal counts as closed, and the written
// manifest's generated_at comes from the injected clock, never the wall
// clock.
func TestRun_WritesManifestFromRealJournalsOnly(t *testing.T) {
	root := writeGenFixture(t,
		[]string{"P1-E01-W1-S01-T1.md", "BLOCKED-P1-E02-W1-S01-T1.md", "DEFECT-something.md"},
		"[]", "")
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))

	if err := run(testOutputWriter(), root, clock); err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "internal", "build", "testdata", "closed-ticket-manifest.json"))
	if err != nil {
		t.Fatalf("reading written manifest: %v", err)
	}
	var got struct {
		GeneratedAt string   `json:"generated_at"`
		Tickets     []string `json:"tickets"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parsing written manifest: %v", err)
	}
	if len(got.Tickets) != 1 || got.Tickets[0] != "P1-E01-W1-S01-T1" {
		t.Fatalf("tickets = %v, want exactly [P1-E01-W1-S01-T1] (BLOCKED- and DEFECT- journals must not count)",
			got.Tickets)
	}
	if got.GeneratedAt != "2026-09-13T00:00:00Z" {
		t.Fatalf("generated_at = %q, want the injected clock's stamp, not the wall clock", got.GeneratedAt)
	}
}

// closedTicketAllowFixture is one allow entry naming P1-E01-W1-S01-T1 as
// its retire_ticket and a caller_site that never exists in the fixture
// root, so once that ticket's journal lands, the entry is orphaned unless
// grandfathered.
const closedTicketAllowFixture = `[
  {
    "symbol": "internal/widget.Foo",
    "reason": "fixture",
    "expected_caller": "fixture",
    "retire_ticket": "P1-E01-W1-S01-T1",
    "caller_site": "internal/widget/never_created.go"
  }
]`

// TestRun_RefusesNewOrphan_RedThenGreen is the mutation proof for gate 3:
// with an empty grandfather list, closing the entry's retire_ticket must
// refuse to write the manifest (RED, with the real message). Adding the
// symbol to the grandfather list, and nothing else, must make the same
// scenario pass (GREEN).
func TestRun_RefusesNewOrphan_RedThenGreen(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))

	t.Run("red: not grandfathered", func(t *testing.T) {
		root := writeGenFixture(t, []string{"P1-E01-W1-S01-T1.md"}, closedTicketAllowFixture, "")
		err := run(testOutputWriter(), root, clock)
		if err == nil {
			t.Fatal("run: expected a refusal, got nil error")
		}
		if !strings.Contains(err.Error(), "refusing to regenerate") ||
			!strings.Contains(err.Error(), "internal/widget.Foo") {
			t.Fatalf("run error = %q, want it to name the refusal and the orphaned symbol", err.Error())
		}
		if _, statErr := os.Stat(
			filepath.Join(root, "internal", "build", "testdata", "closed-ticket-manifest.json"),
		); statErr == nil {
			t.Fatal("run: manifest must not be written when it refuses")
		}
	})

	t.Run("green: grandfathered", func(t *testing.T) {
		root := writeGenFixture(t, []string{"P1-E01-W1-S01-T1.md"}, closedTicketAllowFixture,
			"internal/widget.Foo\n")
		if err := run(testOutputWriter(), root, clock); err != nil {
			t.Fatalf("run: expected success once grandfathered, got: %v", err)
		}
		if _, statErr := os.Stat(
			filepath.Join(root, "internal", "build", "testdata", "closed-ticket-manifest.json"),
		); statErr != nil {
			t.Fatalf("run: manifest should have been written: %v", statErr)
		}
	})
}

// TestScanClosedTickets_MissingDirErrors proves the fail-loud shape: a
// missing journals directory (any CI checkout) is an error, never a
// silent empty result.
func TestScanClosedTickets_MissingDirErrors(t *testing.T) {
	if _, err := scanClosedTickets(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("scanClosedTickets: expected an error for a missing directory")
	}
}

// TestLoadGrandfatherList_MissingFileErrors mirrors the same fail-loud
// shape for the grandfather list.
func TestLoadGrandfatherList_MissingFileErrors(t *testing.T) {
	if _, err := loadGrandfatherList(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("loadGrandfatherList: expected an error for a missing file")
	}
}

// TestScanClosedTickets_SkipsSubdirectories proves the IsDir skip is load
// bearing, not redundant with the filename regex: the subdirectory here
// is NAMED exactly like a real journal (P1-E01-W1-S02-T1.md as a
// directory, not a file), so a scan that forgot to skip directories would
// wrongly count it as a second closed ticket. A directory named to merely
// look unrelated ("not-a-journal") would pass even without the IsDir
// check, since the regex alone would reject it — that would not prove
// this branch does anything.
func TestScanClosedTickets_SkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "P1-E01-W1-S02-T1.md"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "P1-E01-W1-S01-T1.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := scanClosedTickets(dir)
	if err != nil {
		t.Fatalf("scanClosedTickets: %v", err)
	}
	if len(got) != 1 || got[0] != "P1-E01-W1-S01-T1" {
		t.Fatalf("scanClosedTickets = %v, want exactly [P1-E01-W1-S01-T1] (the directory must not match despite its name)", got)
	}
}

// TestRun_MissingJournalsDirReturnsError proves run's own error return
// for scanClosedTickets failing (a checkout with no journals directory
// at all, e.g. a fresh clone before any ticket ever closed).
func TestRun_MissingJournalsDirReturnsError(t *testing.T) {
	root := t.TempDir()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	if err := run(testOutputWriter(), root, clock); err == nil {
		t.Fatal("run: expected an error when the journals directory is missing")
	}
}

// TestRun_MalformedAllowListReturnsError proves run's own error return
// for the test-only allow list failing to parse.
func TestRun_MalformedAllowListReturnsError(t *testing.T) {
	root := writeGenFixture(t, []string{"P1-E01-W1-S01-T1.md"}, "not valid json", "")
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	if err := run(testOutputWriter(), root, clock); err == nil {
		t.Fatal("run: expected an error for a malformed testonly-allow.json")
	}
}

// TestRun_MissingGrandfatherFileReturnsError proves run's own error
// return for the orphan grandfather list failing to read, with a valid
// journals directory and allow list in place so the failure is
// attributable to the grandfather read alone.
func TestRun_MissingGrandfatherFileReturnsError(t *testing.T) {
	root := t.TempDir()
	journalsDir := filepath.Join(root, ".claude", "planning", "p1", "phase", "journals")
	if err := os.MkdirAll(journalsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll journals: %v", err)
	}
	if err := os.WriteFile(filepath.Join(journalsDir, "P1-E01-W1-S01-T1.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("writing journal: %v", err)
	}
	buildDir := filepath.Join(root, "internal", "build")
	// The testdata directory is created (unlike the other fixtures'
	// helper) so a WriteFile failure later in run() cannot masquerade as
	// this test's signal: if the grandfather-read error were ever
	// dropped, run() would otherwise succeed all the way through to a
	// real write, making this test a false pass for the wrong reason.
	if err := os.MkdirAll(filepath.Join(buildDir, "testdata"), 0o755); err != nil {
		t.Fatalf("MkdirAll internal/build/testdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "testonly-allow.json"), []byte("[]"), 0o644); err != nil {
		t.Fatalf("writing testonly-allow.json: %v", err)
	}
	// Deliberately no testdata/testonly-orphan-grandfather.txt written.
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	err := run(testOutputWriter(), root, clock)
	if err == nil {
		t.Fatal("run: expected an error when the grandfather list is missing")
	}
	if !strings.Contains(err.Error(), "grandfather") {
		t.Fatalf("run error = %q, want it to name the grandfather list read", err.Error())
	}
}

// TestRun_WriteFailureReturnsError proves run's own error return when the
// manifest cannot be written: a directory already occupies the exact
// output path, so os.WriteFile refuses (EISDIR), a portable way to force
// a real write failure without touching filesystem permission bits.
func TestRun_WriteFailureReturnsError(t *testing.T) {
	root := writeGenFixture(t, []string{"P1-E01-W1-S01-T1.md"}, "[]", "")
	outPath := filepath.Join(root, "internal", "build", "testdata", "closed-ticket-manifest.json")
	if err := os.Mkdir(outPath, 0o755); err != nil {
		t.Fatalf("Mkdir outPath: %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	if err := run(testOutputWriter(), root, clock); err == nil {
		t.Fatal("run: expected an error when the output path is occupied by a directory")
	}
}

// TestRepoRoot_ResolvesRealCheckoutRoot calls the real, unmodified
// repoRoot against this actual checkout (the only honest way to exercise
// a function whose entire body is "ask git" — no fixture stands in for a
// real repository here) and proves it resolves a real root by checking
// go.mod exists directly under it.
func TestRepoRoot_ResolvesRealCheckoutRoot(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("repoRoot returned %q, which has no go.mod: %v", root, statErr)
	}
}

// TestRepoRoot_NoGitOnPathReturnsError proves the error path: with git
// unresolvable via PATH, repoRoot must fail rather than return a bogus
// root.
func TestRepoRoot_NoGitOnPathReturnsError(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := repoRoot(); err == nil {
		t.Fatal("repoRoot: expected an error with no git on PATH")
	}
}
