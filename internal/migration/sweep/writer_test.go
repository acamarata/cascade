// Purpose: unit and integration tests for row aggregation, Markdown/JSON
// rendering, idempotence, and Run()'s end-to-end exit-code contract.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
)

func TestWriter_InventoryHeaderAndPathRows(t *testing.T) {
	rows := []Row{
		{Term: Term{Term: "zz-fixture-a", Class: "command"}, Path: "internal/a.go", Disposition: DispositionUnresolved, Reason: "no declared v2 replacement"},
		{Term: Term{Term: "zz-fixture-b", Class: "marker"}, Path: "internal/b.go", Disposition: DispositionProvenance},
	}
	p := filepath.Join(t.TempDir(), "inv.md")
	if err := WriteMarkdown(rows, p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "| pointer | path | disposition | replacement | decommission-safe |") {
		t.Fatalf("missing header: %s", body)
	}
	if strings.Count(body, "zz-fixture-a") != 1 || strings.Count(body, "zz-fixture-b") != 1 {
		t.Fatalf("expected exactly one row per (pointer, path): %s", body)
	}
	for _, ln := range strings.Split(body, "\n") {
		if !strings.Contains(ln, "zz-fixture") {
			continue
		}
		for _, tok := range strings.Fields(strings.Trim(ln, "| ")) {
			tok = strings.Trim(tok, "|")
			if _, convErr := strconv.Atoi(tok); convErr == nil {
				t.Fatalf("row %q appears to contain a bare line-number token %q", ln, tok)
			}
		}
	}
}

func TestUnresolvedJSON_ReasonNonEmpty(t *testing.T) {
	for _, reason := range []string{"", "   "} {
		rows := []Row{{Term: Term{Term: "zz-fixture", Class: "command"}, Path: "a.go", Disposition: DispositionUnresolved, Reason: reason}}
		p := filepath.Join(t.TempDir(), "unresolved.json")
		err := WriteUnresolvedJSON(rows, p)
		if err == nil {
			t.Fatalf("reason %q: WriteUnresolvedJSON accepted an UNRESOLVED row with an empty reason", reason)
		}
		if !strings.Contains(err.Error(), "empty reason") || !strings.Contains(err.Error(), "zz-fixture") {
			t.Fatalf("reason %q: error %q does not name the empty reason and the pointer", reason, err)
		}
		if _, statErr := os.Stat(p); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("reason %q: refused write still left %s on disk (stat err=%v)", reason, p, statErr)
		}
	}
	// buildRows (the real production path) always sets a reason for
	// UNRESOLVED, so the real pipeline never trips the refusal above.
	hits := []Hit{{Term: Term{Term: "zz-fixture2", Class: "command", State: ""}, Path: "b.go", Line: 1}}
	built, err := buildRows(hits, map[string]bool{}, &Resolver{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0].Reason == "" {
		t.Fatalf("built=%+v, want a non-empty reason on the UNRESOLVED row", built)
	}
	if err := WriteUnresolvedJSON(built, filepath.Join(t.TempDir(), "unresolved.json")); err != nil {
		t.Fatalf("WriteUnresolvedJSON rejected a row with a reason: %v", err)
	}
}

func TestLoadTermFile_ParsesValidTerms_writerPkgSanity(t *testing.T) {
	// Sanity check that a term round-trips through JSON with the schema
	// writer.go/resolver.go consume (regression guard for field renames).
	term := Term{Term: "zz", Class: "command", Replacement: []string{"a.go"}, Anchor: "A", State: "done"}
	data, err := json.Marshal(term)
	if err != nil {
		t.Fatal(err)
	}
	var back Term
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Term != term.Term || back.Class != term.Class || back.Anchor != term.Anchor ||
		back.State != term.State || strings.Join(back.Replacement, ",") != strings.Join(term.Replacement, ",") {
		t.Fatalf("round-trip mismatch: %+v != %+v", back, term)
	}
}

// --- buildRows aggregation ---

func TestBuildRows_AggregatesByTermAndPathNotLine(t *testing.T) {
	hits := []Hit{
		{Term: Term{Term: "zz", Class: "command"}, Path: "a.go", Line: 1},
		{Term: Term{Term: "zz", Class: "command"}, Path: "a.go", Line: 5},
		{Term: Term{Term: "zz", Class: "command"}, Path: "a.go", Line: 9},
	}
	rows, err := buildRows(hits, map[string]bool{}, &Resolver{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want exactly one aggregated (term, path) row", rows)
	}
}

func TestBuildRows_DoneTermErrorPropagates(t *testing.T) {
	hits := []Hit{{Term: Term{Term: "zz", Class: "command", Replacement: []string{"missing.go"}, Anchor: "X", State: "done"}, Path: "a.go", Line: 1}}
	if _, err := buildRows(hits, map[string]bool{}, &Resolver{Root: t.TempDir()}); err == nil {
		t.Fatal("expected the done-term resolve error to propagate")
	}
}

// --- Run(): end-to-end exit codes ---

func TestSweep_ZeroHitTreeExitsCleanWithEmptyTable(t *testing.T) {
	f := newFixture(t)
	f.source("internal/a.go", "nothing of interest\n")
	f.writeTerms([]Term{{Term: "zz-fixture-nomatch", Class: "marker"}})
	list := f.filesList("internal/a.go")
	if rc := f.run(list); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "docs", "migration", "v1-pointer-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n|") != 2 { // header + separator only
		t.Fatalf("expected an empty table body: %s", data)
	}
}

func TestSweepFailOnUnresolvedExitCode(t *testing.T) {
	f := newFixture(t)
	f.source("internal/a.go", "zz-fixture-open here\n")
	f.writeTerms([]Term{{Term: "zz-fixture-open", Class: "command"}})
	list := f.filesList("internal/a.go")
	if rc := f.run(list, "--fail-on-unresolved"); rc != 3 {
		t.Fatalf("rc=%d, want 3", rc)
	}
}

func TestSweepDeterministic(t *testing.T) {
	f := newFixture(t)
	f.source("internal/a.go", "zz-fixture-x and zz-fixture-y\n")
	f.source("internal/b.go", "zz-fixture-x again\n")
	f.writeTerms([]Term{{Term: "zz-fixture-x", Class: "command"}, {Term: "zz-fixture-y", Class: "command"}})
	list := f.filesList("internal/a.go", "internal/b.go")
	if rc := f.run(list); rc != 0 {
		t.Fatalf("first run rc=%d", rc)
	}
	first, err := os.ReadFile(filepath.Join(f.root, "docs", "migration", "v1-pointer-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if rc := f.run(list); rc != 0 {
		t.Fatalf("second run rc=%d", rc)
	}
	second, err := os.ReadFile(filepath.Join(f.root, "docs", "migration", "v1-pointer-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("second run drifted:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestSweepRejectsFalseDone(t *testing.T) {
	f := newFixture(t)
	f.source("internal/a.go", "zz-fixture-done here\n")
	f.writeTerms([]Term{{Term: "zz-fixture-done", Class: "command", Replacement: []string{"internal/missing.go"}, Anchor: "X", State: "done"}})
	list := f.filesList("internal/a.go")
	if rc := f.run(list); rc != 2 {
		t.Fatalf("rc=%d, want 2 (done term, missing replacement file)", rc)
	}
}

func TestSweepUnresolvedAndMissingInput(t *testing.T) {
	f := newFixture(t)
	f.writeTerms([]Term{{Term: "zz-fixture-unused", Class: "command"}})
	list := f.filesList("internal/a.go") // listed but never written to disk
	rc, stderr := f.runCaptured(list)
	if rc != 2 {
		t.Fatalf("rc=%d, want 2 (listed tracked file does not exist on disk)", rc)
	}
	if !strings.Contains(stderr, "internal/a.go") {
		t.Fatalf("stderr %q does not name the missing path internal/a.go", stderr)
	}
}

// TestSweep_UnreadableWalkedFileExits2: a listed file the sweep cannot
// read fails the run with exit 2 and names the path. Unix only: chmod 000
// does not deny reads on windows.
func TestSweep_UnreadableWalkedFileExits2(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("TestSweep_UnreadableWalkedFileExits2: unix-only gate; chmod 000 cannot deny reads on windows")
	}
	f := newFixture(t)
	f.writeTerms([]Term{{Term: "zz-fixture-unused", Class: "command"}})
	f.source("internal/ok.go", "nothing here\n")
	f.source("internal/locked.go", "zz-fixture-unused\n")
	locked := filepath.Join(f.root, "internal", "locked.go")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
	if _, err := os.ReadFile(locked); err == nil { //nolint:gosec // test fixture path
		t.Skip("TestSweep_UnreadableWalkedFileExits2: chmod 000 did not deny reads (running as root)")
	}
	rc, stderr := f.runCaptured(f.filesList("internal/locked.go", "internal/ok.go"))
	if rc != 2 {
		t.Fatalf("rc=%d, want 2 (unreadable walked file)", rc)
	}
	// The relative path must appear in the sweep's own message: the OS
	// error alone carries the absolute path, which would hide a dropped rel.
	if !strings.Contains(stderr, "reading internal/locked.go") {
		t.Fatalf("stderr %q does not name the unreadable relative path (want \"reading internal/locked.go\")", stderr)
	}
}

func TestUnresolvedJSONEqualsUnresolvedRows(t *testing.T) {
	f := newFixture(t)
	f.source("internal/a.go", "zz-fixture-open mention\n")
	f.writeTerms([]Term{{Term: "zz-fixture-open", Class: "command"}})
	list := f.filesList("internal/a.go")
	if rc := f.run(list); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "docs", "migration", "unresolved.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Pointer, Path, Reason string
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Pointer != "zz-fixture-open" || entries[0].Path != "internal/a.go" || entries[0].Reason == "" {
		t.Fatalf("entries=%+v", entries)
	}
}
