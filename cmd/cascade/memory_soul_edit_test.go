// Purpose: unit tests for `cascade memory soul edit` — the automation
//
//	parity pair (§5 rule 8): --content applies a file with no editor at
//	all, and CASCADE_NO_INPUT=1 fails BEFORE any editor subprocess
//	exists. Split from memory_soul_test.go for the 300-line file cap.
//
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSoulEditContentFlagOpensNoEditor is the automation-parity half: a
// file's contents are applied with no editor, no environment read and no
// terminal, which is what makes it usable from a script.
func TestSoulEditContentFlagOpensNoEditor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "soul.md")
	if err := os.WriteFile(path, []byte("from a file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	h := &soulHarness{results: map[string]any{
		memory.MethodSoulEdit: memory.SoulEditResult{Version: 7},
	}}
	stdout, _, err := h.run(t, "edit", "--content", path)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if h.editorRuns != 0 {
		t.Fatalf("--content launched an editor %d times", h.editorRuns)
	}
	if len(h.calls) != 1 || h.calls[0].Method != memory.MethodSoulEdit {
		t.Fatalf("calls = %+v", h.calls)
	}
	params, ok := h.calls[0].Params.(memory.SoulEditParams)
	if !ok || params.Body != "from a file\n" {
		t.Fatalf("params = %+v", h.calls[0].Params)
	}
	if !strings.Contains(stdout, "version 7") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestSoulEditContentFlagMissingFile proves an unreadable --content file
// is an invalid-input refusal and never reaches the store.
func TestSoulEditContentFlagMissingFile(t *testing.T) {
	h := &soulHarness{}
	_, _, err := h.run(t, "edit", "--content", filepath.Join(t.TempDir(), "absent.md"))
	if err == nil {
		t.Fatal("a missing --content file was accepted")
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, want invalid-input", kind)
	}
	if len(h.calls) != 0 {
		t.Fatalf("a failed read still called the store: %+v", h.calls)
	}
}

// TestSoulEditNoInputFailsBeforeAnyEditorExists is the other
// automation-parity half. The assertion that matters is editorRuns == 0:
// an automation environment must get a refusal it can read rather than a
// subprocess waiting forever on a terminal that is not there.
func TestSoulEditNoInputFailsBeforeAnyEditorExists(t *testing.T) {
	h := &soulHarness{env: map[string]string{"CASCADE_NO_INPUT": "1"}}
	_, _, err := h.run(t, "edit")
	if err == nil {
		t.Fatal("CASCADE_NO_INPUT=1 did not make the editor path fail")
	}
	if h.editorRuns != 0 {
		t.Fatalf("the guard fired after launching %d editors", h.editorRuns)
	}
	if len(h.calls) != 0 {
		t.Fatalf("the guard fired after calling the store: %+v", h.calls)
	}
	if !strings.Contains(err.Error(), "hard error") {
		t.Fatalf("the refusal does not name a hard error: %v", err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, want invalid-input", kind)
	}
}

// TestSoulEditThroughTheEditor walks the interactive path: the current
// document is fetched, handed to the editor, and what the editor saved is
// what gets applied.
func TestSoulEditThroughTheEditor(t *testing.T) {
	h := &soulHarness{
		results: map[string]any{
			memory.MethodSoulShow: memory.SoulShowResult{Body: "before", Version: 1},
			memory.MethodSoulEdit: memory.SoulEditResult{Version: 2},
		},
		editWith: "after the edit\n",
	}
	if _, _, err := h.run(t, "edit"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if h.editorRuns != 1 {
		t.Fatalf("editor ran %d times, want 1", h.editorRuns)
	}
	if len(h.calls) != 2 || h.calls[0].Method != memory.MethodSoulShow {
		t.Fatalf("calls = %+v", h.calls)
	}
	params, ok := h.calls[1].Params.(memory.SoulEditParams)
	if !ok || params.Body != "after the edit\n" {
		t.Fatalf("params = %+v", h.calls[1].Params)
	}
}

// TestSoulEditFromNothingStartsEmpty proves the first `soul edit` on a
// machine that has never written a SOUL opens an empty document rather
// than failing: writing it for the first time is the point.
func TestSoulEditFromNothingStartsEmpty(t *testing.T) {
	h := &soulHarness{
		err:      cascade.Wrapf(cascade.KindNotFound, memory.ErrNoSoulDocument, "no soul yet"),
		editWith: "my first soul\n",
	}
	// The show call fails not-found; the edit call must still be attempted.
	_, _, _ = h.run(t, "edit")
	if h.editorRuns != 1 {
		t.Fatalf("editor ran %d times, want 1", h.editorRuns)
	}
	if len(h.calls) != 2 {
		t.Fatalf("calls = %+v, want show then edit", h.calls)
	}
	params, ok := h.calls[1].Params.(memory.SoulEditParams)
	if !ok || params.Body != "my first soul\n" {
		t.Fatalf("params = %+v", h.calls[1].Params)
	}
}

// TestSoulEditEditorFailureIsScrubbed proves an editor failure comes back
// as a scrubbed taxonomy error rather than a raw exec diagnostic naming
// the operator's temp path.
func TestSoulEditEditorFailureIsScrubbed(t *testing.T) {
	h := &soulHarness{
		results:   map[string]any{memory.MethodSoulShow: memory.SoulShowResult{Body: "x"}},
		editorErr: cascade.Newf(cascade.KindUnavailable, "run $EDITOR: /Users/someone/bin/vi failed"),
	}
	_, _, err := h.run(t, "edit")
	if err == nil {
		t.Fatal("an editor failure was swallowed")
	}
	if strings.Contains(err.Error(), "/Users/someone") {
		t.Fatalf("the diagnostic carried a machine path: %v", err)
	}
	if len(h.calls) != 1 {
		t.Fatalf("a failed edit still wrote: %+v", h.calls)
	}
}
