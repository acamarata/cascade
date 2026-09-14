package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/plugins/claude"
)

// Purpose (this file): cover the cascade-claude composition root — the two
//   adapters it builds and, more importantly, the init() that installs
//   them. A wiring file whose init() silently failed to run would leave the
//   plugin present but inert, which is precisely the failure the seams'
//   loud "not wired" defaults exist to make visible.
// SPORT: internal/plugins cascade-claude wiring tests (ADD) — P1-E16-W4-S34-T1.

// TestClaudeGeneratorIsWiredToTheRealWriter proves init() replaced the
// plugin's default generator with a real one. It asserts on BEHAVIOUR
// rather than comparing function values (Go cannot compare funcs): the
// unwired default fails with a "not wired" error for every input, so any
// outcome that is not that error proves a real generator is installed.
func TestClaudeGeneratorIsWiredToTheRealWriter(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nSome repo-tier content.\n")

	files, err := claude.Generate(context.Background(), dir)
	if err != nil {
		t.Fatalf("the wired generator returned an error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the wired generator produced no files for a tier with real content")
	}
	for _, f := range files {
		if !filepath.IsAbs(f.Path) {
			t.Errorf("generated file path = %q, want an absolute path", f.Path)
		}
		if len(f.Content) == 0 {
			t.Errorf("generated file %q has no content", f.Path)
		}
	}
}

// TestClaudeGeneratorAdapterMapsEveryFile drives the adapter directly to
// prove the translation into the plugin's own internal/-free vocabulary
// carries BOTH fields. An adapter that dropped Content would still return
// the right number of files, so the count alone is not the assertion.
func TestClaudeGeneratorAdapterMapsEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	files, err := harnessGeneratorCC(&casctx.CCInstructionWriter{})(context.Background(), dir)
	if err != nil {
		t.Fatalf("harnessGeneratorCC: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("harnessGeneratorCC produced no files")
	}
	for _, f := range files {
		if f.Path == "" || len(f.Content) == 0 {
			t.Fatalf("adapter dropped a field: %+v", f)
		}
	}
}

// TestClaudeGeneratorAdapterPropagatesError asserts a failure from the
// underlying writer reaches the plugin unchanged rather than being
// swallowed into an empty-but-successful install.
func TestClaudeGeneratorAdapterPropagatesError(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	wantErr := errors.New("fake generator: deliberate failure")
	_, err := harnessGeneratorCC(fakeHarnessGen{err: wantErr})(context.Background(), dir)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want the writer's own error", err)
	}
}

// TestClaudeHookPackIsRegistered proves the R-16.48 RegisterPack call ran.
// Before this ticket hookpacks.SessionsPack() was fixture-backed and
// complete but registered by nothing, so the hooks it describes were never
// installed by anything — a dead surface in the registry itself.
func TestClaudeHookPackIsRegistered(t *testing.T) {
	rendered := hookpacks.DefaultRegistry.Render("/run/cascade.sock")
	if len(rendered) == 0 {
		t.Fatal("the default hook registry rendered nothing; the cascade-claude pack is not registered")
	}
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(rendered, &doc); err != nil {
		t.Fatalf("decode the rendered hook config: %v", err)
	}
	// SessionsPack's event types are each backed by a captured fixture;
	// asserting on them rather than on a count keeps this from passing on
	// some other pack that happens to be registered.
	for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		if len(doc.Hooks[event]) == 0 {
			t.Errorf("the rendered hook config carries no %s entry", event)
		}
	}
}

// TestClaudeHookRendererIsWired proves init() installed the renderer seam,
// again by behaviour: the unwired default reports "not wired" for every
// socket.
func TestClaudeHookRendererIsWired(t *testing.T) {
	rendered, err := claude.RenderHookPack("/run/cascade.sock")
	if err != nil {
		t.Fatalf("the wired renderer returned an error: %v", err)
	}
	if !strings.Contains(string(rendered), "/run/cascade.sock") {
		t.Fatalf("the rendered config does not carry the socket it was rendered for:\n%s", rendered)
	}
}

// TestRenderDefaultHookPacksTranslatesFailClosedNil is the adapter's whole
// reason for existing. HookRegistry.Render is fail-closed: it returns nil
// rather than a partial configuration. The plugin's seam reports failure as
// an error, so a nil that crossed unchanged would arrive at the installer
// as "no configuration" — the silent under-install the fail-closed design
// exists to prevent.
func TestRenderDefaultHookPacksTranslatesFailClosedNil(t *testing.T) {
	if got := hookpacks.DefaultRegistry.Render(""); got != nil {
		t.Fatalf("precondition failed: Render(\"\") = %q, want nil (fail-closed)", got)
	}
	rendered, err := renderDefaultHookPacks("")
	if err == nil {
		t.Fatal("renderDefaultHookPacks(\"\") succeeded, want the fail-closed nil translated into an error")
	}
	if rendered != nil {
		t.Fatalf("renderDefaultHookPacks returned %q alongside its error, want nil", rendered)
	}
}
