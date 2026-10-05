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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/pkg/cascade"
)

// socketTemplatePack is a two-descriptor pack whose commands carry the socket
// placeholder, so the renderer's substitution is exercised independently of
// any shipped pack.
func socketTemplatePack() hookpacks.HookPack {
	tmpl := "probe --unix-socket '{{CASCADE_SOCKET_PATH}}' http://cascade.sock/rpc"
	return hookpacks.HookPack{Name: "socket-template", Descriptors: []hookpacks.HookDescriptor{
		{EventType: hookpacks.EventStop, CommandTemplate: tmpl},
		{EventType: hookpacks.EventPreToolUse, CommandTemplate: tmpl},
	}}
}

// checkCompletionExecutableSymlink re-runs the absolute-path proof through
// an installation symlink on darwin, where os.Executable preserves that path.
func checkCompletionExecutableSymlink(t *testing.T, exe string) {
	t.Helper()
	if runtime.GOOS != "darwin" || filepath.Base(exe) == "installed-cascade" {
		return
	}
	home := t.TempDir()
	link := filepath.Join(home, "installed-cascade")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "-test.run=^TestCompletionHookCommandUsesAbsolutePath$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render through installation symlink: %v\n%s", err, out)
	}
}

func TestRenderer_RejectsUnquotedSocketPlaceholder(t *testing.T) {
	for _, tmpl := range []string{
		"probe {{CASCADE_SOCKET_PATH}}",
		`probe "{{CASCADE_SOCKET_PATH}}"`,
		"probe '{{CASCADE_SOCKET_PATH}}' {{CASCADE_SOCKET_PATH}}",
	} {
		pack := socketTemplatePack()
		pack.Descriptors[1].CommandTemplate = tmpl
		rendered, err := pack.Render("/tmp/$(touch unwanted).sock")
		if err == nil || rendered != nil {
			t.Fatalf("template %q: rendered=%s err=%v, want atomic refusal", tmpl, rendered, err)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("template %q: err=%v, want invalid-input", tmpl, err)
		}
		if !strings.Contains(err.Error(), "socket placeholder must be single-quoted") {
			t.Fatalf("template %q: wrong refusal: %v", tmpl, err)
		}
	}
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

// printfPack renders to a command that prints its two substituted words one
// per line, so a test can run it under /bin/sh and read back what the shell
// actually saw.
func printfPack() hookpacks.HookPack {
	return hookpacks.HookPack{Name: "printf", Descriptors: []hookpacks.HookDescriptor{{
		EventType: hookpacks.EventStop, CommandTemplate: "printf '%s\\n' {{CASCADE_BIN_PATH}} '{{CASCADE_SOCKET_PATH}}'",
	}}}
}

// TestRenderer_BinaryAndSocketReachShellVerbatim runs the rendered command
// under /bin/sh with paths full of shell metacharacters: each must arrive as
// exactly one word, unexpanded, and a placeholder spelled inside one
// substituted value must not be substituted again.
func TestRenderer_BinaryAndSocketReachShellVerbatim(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh in this environment")
	}
	for name, tc := range map[string]struct{ bin, sock string }{
		"spaces":         {"/opt/my cascade/bin", "/tmp/my dir/s.sock"},
		"single_quote":   {"/opt/it's/cascade", "/tmp/it's/s.sock"},
		"substitution":   {"/opt/$(echo pwned)/cascade", "/tmp/`echo pwned`/s.sock"},
		"quote_break":    {`/opt/';echo pwned;'/c`, `/tmp/';echo pwned;'/s`},
		"double_quote":   {`/opt/"x"/c`, `/tmp/"y"/s`},
		"newline":        {"/opt/a\nb/c", "/tmp/a\nb/s"},
		"backslash":      {`/opt/a\b/c`, `/tmp/a\\b/s`},
		"placeholders":   {"/opt/{{CASCADE_SOCKET_PATH}}/c", "/tmp/{{CASCADE_BIN_PATH}}/s"},
		"unicode":        {"/opt/日本/c", "/tmp/😀/s"},
		"glob_and_tilde": {"/opt/*/~/c", "/tmp/[a-z]?/s"},
	} {
		t.Run(name, func(t *testing.T) {
			pack := printfPack()
			rendered, err := pack.RenderWithBinary(tc.sock, tc.bin)
			if err != nil {
				t.Fatalf("RenderWithBinary: %v", err)
			}
			var entry struct {
				Hooks []struct{ Command string } `json:"hooks"`
			}
			if err := json.Unmarshal(rendered[0], &entry); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("/bin/sh", "-c", entry.Hooks[0].Command).Output()
			if err != nil {
				t.Fatalf("run %q: %v", entry.Hooks[0].Command, err)
			}
			if want := tc.bin + "\n" + tc.sock + "\n"; string(out) != want {
				t.Fatalf("shell saw %q, want %q (rendered %q)", out, want, entry.Hooks[0].Command)
			}
		})
	}
}

// TestRenderer_BinaryLookupOnlyForPacksThatNameIt proves a pack without the
// binary placeholder renders with no executable lookup and no binary path at
// all, and a pack that names it refuses a relative path.
func TestRenderer_BinaryLookupOnlyForPacksThatNameIt(t *testing.T) {
	plain := socketTemplatePack()
	if _, err := plain.RenderWithBinary("/tmp/s.sock", ""); err != nil {
		t.Fatalf("a pack that never names the binary must render without one: %v", err)
	}
	if _, err := printfPack().RenderWithBinary("/tmp/s.sock", "relative/cascade"); !errors.Is(err, hookpacks.ErrBinaryPathNotAbsolute) {
		t.Fatalf("relative binary path: err = %v, want ErrBinaryPathNotAbsolute", err)
	}
	if _, err := printfPack().RenderWithBinary("", "/opt/cascade"); !errors.Is(err, hookpacks.ErrEmptySocketPath) {
		t.Fatalf("empty socket path: err = %v, want ErrEmptySocketPath", err)
	}
}
