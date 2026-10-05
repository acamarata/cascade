// Purpose: HookPack.Render's socket-path substitution, the empty-socket
//
//	refusal, structural parseability of the rendered configuration entry
//	(this ticket's CONTRACT DEVIATION on the missing settings-file
//	fixture, see renderer.go's header), and the bounded, non-gating shape
//	of the sessions pack's rendered commands. The socket-substitution cases
//	use a local template pack: the sessions pack's commands resolve the
//	daemon socket at hook time and carry no placeholder.
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// socketTemplatePack is a two-descriptor pack whose commands carry the socket
// placeholder, so the renderer's substitution is exercised independently of
// any shipped pack.
func socketTemplatePack() hookpacks.HookPack {
	tmpl := "probe --unix-socket {{CASCADE_SOCKET_PATH}} http://cascade.sock/rpc"
	return hookpacks.HookPack{Name: "socket-template", Descriptors: []hookpacks.HookDescriptor{
		{EventType: hookpacks.EventStop, CommandTemplate: tmpl},
		{EventType: hookpacks.EventPreToolUse, CommandTemplate: tmpl},
	}}
}

func TestRenderer_SubstitutesSocketPathForEveryDescriptor(t *testing.T) {
	pack := socketTemplatePack()
	rendered, err := pack.Render("/tmp/cascade-test.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(rendered) != len(pack.Descriptors) {
		t.Fatalf("Render returned %d entries, want %d", len(rendered), len(pack.Descriptors))
	}
	for i, raw := range rendered {
		var entry struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("entry %d does not parse: %v", i, err)
		}
		if !strings.Contains(entry.Hooks[0].Command, "/tmp/cascade-test.sock") {
			t.Fatalf("entry %d command = %q, want the socket path substituted in", i, entry.Hooks[0].Command)
		}
		if strings.Contains(entry.Hooks[0].Command, "{{CASCADE_SOCKET_PATH}}") {
			t.Fatalf("entry %d command = %q, still carries the unrendered placeholder", i, entry.Hooks[0].Command)
		}
		if entry.Hooks[0].Type != "command" {
			t.Fatalf("entry %d hook type = %q, want %q", i, entry.Hooks[0].Type, "command")
		}
	}
}

func TestRenderer_EmptySocketPathRefused(t *testing.T) {
	pack := socketTemplatePack()
	if _, err := pack.Render(""); err == nil {
		t.Fatal("Render(\"\") should refuse with ErrEmptySocketPath")
	}
}

// TestRenderer_SessionsCommandsAreBoundedAndNonGating asserts, structurally,
// that every rendered sessions command is the hook-event command with the
// harness-side timeout stated, and that none can fail its host through shell
// plumbing: the command itself exits 0 on every path (proved against a live
// daemon in cmd/cascade), so the template adds no `|| true` and no exit code.
func TestRenderer_SessionsCommandsAreBoundedAndNonGating(t *testing.T) {
	pack := hookpacks.SessionsPack()
	rendered, err := pack.Render("/tmp/cascade-test.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i, raw := range rendered {
		var entry struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		hook := entry.Hooks[0]
		want := "cascade fleet sessions hook-event " + string(pack.Descriptors[i].EventType)
		if hook.Command != want {
			t.Fatalf("entry %d command = %q, want %q", i, hook.Command, want)
		}
		if hook.Timeout != hookpacks.SessionsHookTimeoutSeconds {
			t.Fatalf("entry %d timeout = %d, want %d", i, hook.Timeout, hookpacks.SessionsHookTimeoutSeconds)
		}
	}
}
