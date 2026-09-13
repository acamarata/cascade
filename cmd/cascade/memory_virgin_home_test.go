package main

// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's end-to-end
// regression test: drive the REAL production command tree (newRootCmd
// with productionMemoryDeps, not a fake-Paths stub) against a CASCADE_HOME
// that has never been created — the exact virgin-HOME state the DEFECT's
// own reproduction used ("warning: daemon not running; running in
// embedded (daemonless) mode" immediately followed by "memory.remember
// daemon not running or unreachable"). Reuses execRootProductionVirginHome
// (context_slice_virgin_home_test.go) verbatim, the same helper
// recall_virgin_home_test.go reuses for its own sibling proof.
//
// SPORT: cmd.cascade.cmd.memory (FIX, embedded-path routing test).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMemoryRememberVirginHomeNeverDials is the primary regression proof:
// on a virgin HOME with no daemon, `cascade memory remember` must write
// the record and print its canonical address — never the dial failure the
// shipped binary produced.
func TestMemoryRememberVirginHomeNeverDials(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	if _, err := os.Stat(virginHome); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: virginHome must not exist yet, stat err=%v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "memory", "remember", "a fresh-install note", "--name", "first-note")
	if err != nil {
		t.Fatalf("cascade memory remember on a virgin HOME: %v\noutput:\n%s", err, got)
	}
	if strings.Contains(got, "daemon not running or unreachable") || strings.Contains(got, "dial unix") {
		t.Fatalf("got = %v, still the dial-error shape DEFECT-cli-surfaces-promise-embedded-mode.md reported", got)
	}
	if !strings.Contains(got, "project/first-note") {
		t.Fatalf("output = %q, want the canonical address project/first-note", got)
	}

	recordPath := filepath.Join(virginHome, "memory", "project", "first-note.md")
	if _, statErr := os.Stat(recordPath); statErr != nil {
		t.Errorf("cascade memory remember did not write %s: stat err=%v", recordPath, statErr)
	}
}

// TestMemoryRecallVirginHomeFindsWhatWasRemembered proves the embedded
// path is the SAME store across invocations, not a throwaway per-call
// composition: a record written by one `memory remember` invocation is
// found by the NEXT process's `memory recall`, exactly as it would be
// against a live daemon's single on-disk store. This is also the DEFECT's
// own stated reason `memory remember` is the more serious of the three
// surfaces: without it, a fresh-install `recall` has nothing to find.
func TestMemoryRecallVirginHomeFindsWhatWasRemembered(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")

	if _, err := execRootProductionVirginHome(t, virginHome, "memory", "remember", "the widget ships in blue", "--name", "widget-color"); err != nil {
		t.Fatalf("cascade memory remember: %v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "memory", "recall", "widget")
	if err != nil {
		t.Fatalf("cascade memory recall on a virgin HOME: %v\noutput:\n%s", err, got)
	}
	if !strings.Contains(got, "project/widget-color") {
		t.Fatalf("recall output = %q, want it to find project/widget-color", got)
	}
}
