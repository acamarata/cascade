//go:build !windows

// Purpose: the real daemon wiring path composes the one hooks Dispatcher
// with the plugin-call and agent-note runners, both bound through the
// rehydration seam: one event fires both hooks and each leaves its effect.
// The production plugin-call entry is the library hooks.PluginCallRunner
// over hookPluginRunner, not a cmd-local runner.
// Inputs: composeElevated over a temp home, the real cascade-github plugin
// as the trusted fixture, config.accept_loosening over the elevated caller.
// SPORT: cmd/cascade hooks composition tests (P1-SUP-02).
package main

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
)

const compositionNoteHook = "[[hooks]]\nid = \"jot\"\nnamespace = \"hooktest\"\ntrigger = \"hooktest.go\"\naction_type = \"agent-note\"\n" +
	"action_params = { note = \"composed-note\", body = \"the composed dispatcher ran\" }\n"

func TestDaemonHookCompositionWiring(t *testing.T) {
	isolateHome(t)
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, hookTestPlugin)
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	wiring := captureWiring(t)
	c := composeElevated(t, true)
	manifest, err := os.ReadFile("../../plugins/github/manifest.toml")
	hookMust(t, err)
	attestedAdd(t, c.Call, hookTestPlugin, string(manifest))
	body := fireHookBody + compositionNoteHook
	writeConfig(t, c.Deps.Paths.ConfigPath(), body)
	var accepted acceptLooseningResult
	hookMust(t, c.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(body), &accepted))
	if !accepted.Reload.Accepted {
		t.Fatalf("config.accept_loosening did not apply the hooks: %+v", accepted)
	}
	var listed hooksListResult
	hookWaitFor(t, "both accepted hooks to be registered", func() bool {
		return callOwner(t, c.Registry, hooksListMethod, &listed) == nil && len(listed.Hooks) == 2
	})
	w := *wiring
	assertLibraryPluginRunner(t, w)
	if _, err := w.Bus.Publish(context.Background(), fireTriggerNS, fireTriggerKind, "test", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var fires hooksFiresResult
	hookWaitFor(t, "a fire record per hook", func() bool {
		return callOwner(t, c.Registry, hooksFiresMethod, &fires) == nil && len(fires.Fires) >= 2
	})
	seen := map[string]hooks.ActionType{}
	for _, f := range fires.Fires {
		if f.ResultCode != hooks.ResultSuccess {
			t.Fatalf("fire %+v; want success", f)
		}
		seen[f.HookID] = f.ActionType
	}
	if seen["greet"] != hooks.ActionTypePluginCall || seen["jot"] != hooks.ActionTypeAgentNote {
		t.Fatalf("fired hooks = %v; want greet as plugin-call and jot as agent-note", seen)
	}
	note, err := memory.NewFileStore(memoryStoreDir(c.Deps.Paths), w.Clock).Read(context.Background(), memory.KindProject, "composed-note")
	hookMust(t, err)
	if note.Provenance.SessionID != "hook:jot" || note.Body != "the composed dispatcher ran" {
		t.Fatalf("stored note = %+v; want the jot hook's body with hook provenance", note)
	}
}

// assertLibraryPluginRunner proves the daemon's plugin-call table entry is
// the runner hooks.PluginCallRunner builds, wrapped only by the tool origin.
func assertLibraryPluginRunner(t *testing.T, w *daemonWiring) {
	t.Helper()
	want := reflect.TypeOf(hooks.PluginCallRunner(hookPluginRunner{}))
	built, err := buildPluginCallRunner(w)
	hookMust(t, err)
	runners, err := hookRunnersFor(w)
	hookMust(t, err)
	wrapped, ok := runners[hooks.ActionTypePluginCall].(toolOriginRunner)
	if !ok || reflect.TypeOf(built) != want || reflect.TypeOf(wrapped.inner) != want {
		t.Fatalf("plugin-call runner = %T (table %T); want %v under the tool origin", built, runners[hooks.ActionTypePluginCall], want)
	}
}
