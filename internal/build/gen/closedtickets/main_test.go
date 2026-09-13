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
