package plugins

// Purpose: the two collaborators the composition root injects into
//   cascade-claude (P1-E16-W4-S35-T5) — the derived daemon socket and the
//   managed-block instruction writer.
// Constraints: both exist because the plugin may not import internal/**
//   (Art.10.2), so they are the only place their behaviour can be
//   asserted. Both were shipped uncovered, and both encode a decision:
//   the socket is DERIVED rather than demanded, and a hand-edited
//   instruction file is PRESERVED rather than overwritten or fatal.
// SPORT: internal/plugins cascade-claude wiring (ADD) — P1-E16-W4-S35-T5.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/claude"
)

// TestTheSocketIsDerivedFromTheCascadeHome: the plugin's own resolver can
// reach nothing but the override variable, and an unset override made the
// hook-pack install refuse on every machine whose operator had never
// exported one. This seam is what makes "derive it" an available answer,
// so a resolver returning an empty path is as much a failure as an error.
func TestTheSocketIsDerivedFromTheCascadeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
	t.Setenv("CASCADE_SOCKET", "")

	socket, err := runtimeSocket()
	if err != nil {
		t.Fatalf("runtimeSocket with no override set: %v", err)
	}
	if socket == "" {
		t.Fatal("runtimeSocket returned an empty path; the renderer refuses an empty socket, " +
			"so this is the same failure one step later")
	}
	if !strings.HasPrefix(socket, home) {
		t.Errorf("socket = %q, want it under the cascade home this test pinned (%q)", socket, home)
	}
}

// TestTheOverrideStillWins keeps the derivation from swallowing an
// operator who DID say where the socket is.
func TestTheOverrideStillWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	override := filepath.Join(t.TempDir(), "elsewhere.sock")
	t.Setenv("CASCADE_SOCKET", override)

	socket, err := runtimeSocket()
	if err != nil {
		t.Fatalf("runtimeSocket: %v", err)
	}
	if socket != override {
		t.Errorf("socket = %q, want the override %q", socket, override)
	}
}

// TestAHandEditedInstructionFileIsPreservedNotOverwritten is the rule 08
// §2 and Z/S-53.T3 both state, at the seam that enforces it.
//
// Three outcomes, and they are different: a new file is CREATED, an
// unchanged one is a no-op, and an edited one is PRESERVED — reported,
// not written, and not an error either. Failing the whole setup run would
// be a fourth behaviour neither the rule nor the operator asked for.
func TestAHandEditedInstructionFileIsPreservedNotOverwritten(t *testing.T) {
	// The content comes from the REAL generator, because the managed
	// block's markers are part of what it produces and the merge reads
	// them. Hand-written content with no markers takes a different branch
	// and would prove nothing about the rule under test.
	// HOME pinned: unpinned, discovery reaches the DEVELOPER's own global
	// tier and the generator returns their file first (Art.7.1). The
	// project tier is the only one this test wants to be about.
	pinHome(t, t.TempDir())
	project := t.TempDir()
	writeTierFile(t, project, "# Repo Instructions\n\nthe managed content.\n")
	files, err := claude.Generate(context.Background(), project)
	if err != nil || len(files) == 0 {
		t.Fatalf("the wired generator produced nothing to write: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	generated := files[0].Content

	created, err := managedBlockWriter(path, generated)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !created.Changed || created.Preserved {
		t.Errorf("first write = %+v, want Changed with nothing preserved", created)
	}

	again, err := managedBlockWriter(path, generated)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if again.Changed || again.Preserved {
		t.Errorf("an unchanged rewrite = %+v, want a no-op", again)
	}

	// Edit INSIDE the managed block, which is what the drift check reads.
	body := mustReadFile(t, path)
	edited := strings.Replace(body, "the managed content.", "a line somebody changed by hand.", 1)
	if edited == body {
		t.Fatal("the fixture did not actually edit the managed block")
	}
	mustWriteFile(t, path, edited)

	preserved, err := managedBlockWriter(path, generated)
	if err != nil {
		t.Fatalf("writing over a hand-edited file returned an error, want a preserved report: %v", err)
	}
	if !preserved.Preserved || preserved.Changed {
		t.Errorf("over a hand-edited file = %+v, want Preserved and not Changed", preserved)
	}
	if preserved.Reason == "" {
		t.Error("a preserved file carries no reason; the operator is told nothing")
	}
	if got := mustReadFile(t, path); got != edited {
		t.Errorf("the edit was destroyed.\n--- got ---\n%s\n--- want ---\n%s", got, edited)
	}
}

// TestAWriteFailureIsStillAnError: only the hand-edited conflict becomes a
// preserved report. Everything else has to surface, or a setup run reports
// success over a filesystem that refused it.
func TestAWriteFailureIsStillAnError(t *testing.T) {
	dir := t.TempDir()
	// A path whose parent is a FILE cannot be created, on every platform.
	blocker := filepath.Join(dir, "not-a-directory")
	mustWriteFile(t, blocker, "x")

	if _, err := managedBlockWriter(filepath.Join(blocker, "CLAUDE.md"), []byte("x")); err == nil {
		t.Error("a write into an impossible path reported success")
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a path this test composed.
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestAnUnresolvableHomeRefusesRatherThanGuessing: with no home to derive
// from, the resolver must return an error. A fallback to some default
// would install a hook pack pointing at a socket nothing listens on, which
// fails silently at session time — the failure mode the whole
// derive-don't-demand seam exists to avoid.
func TestAnUnresolvableHomeRefusesRatherThanGuessing(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("CASCADE_HOME", "")
	t.Setenv("CASCADE_SOCKET", "")

	socket, err := runtimeSocket()
	if err == nil {
		t.Fatalf("runtimeSocket resolved %q with no home to derive from", socket)
	}
	if socket != "" {
		t.Errorf("runtimeSocket returned %q alongside an error; a caller reading past the error "+
			"would install a hook pack pointing at it", socket)
	}
}
