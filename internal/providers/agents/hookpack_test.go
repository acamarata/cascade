package agents

// Purpose: RenderAgentHookPack writes the three relay entries, merges into an
//   existing pack without dropping anything, is idempotent, and refuses an
//   unsafe binary path or a symlinked pack.
// Constraints: temp directories only; the binary path is a synthetic temp path.
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func kindOf(err error) cascade.Kind {
	k, _ := cascade.KindOf(err)
	return k
}

type packEntry struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

func readPack(t *testing.T, dir string) (map[string]json.RawMessage, map[string][]packEntry) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("settings.json is not JSON: %v", err)
	}
	var hooks map[string][]packEntry
	if err := json.Unmarshal(doc["hooks"], &hooks); err != nil {
		t.Fatalf("hooks: %v", err)
	}
	return doc, hooks
}

func relayCommands(entries []packEntry, suffix string) int {
	n := 0
	for _, e := range entries {
		for _, h := range e.Hooks {
			if strings.HasSuffix(h.Command, suffix) {
				n++
			}
		}
	}
	return n
}

func TestRenderAgentHookPackWritesRelayEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pack")
	exe := filepath.Join(t.TempDir(), "cascade")
	if err := RenderAgentHookPack(dir, exe); err != nil {
		t.Fatal(err)
	}
	_, hooks := readPack(t, dir)
	word := exe // a path of plain characters is one shell word as it stands
	if goruntime.GOOS == "windows" {
		word = `"` + exe + `"` // a drive colon and backslashes are not plain: the word is double-quoted
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		entries := hooks[ev]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("%s: %+v, want one entry with one hook", ev, entries)
		}
		h := entries[0].Hooks[0]
		if h.Type != "command" || h.Command != word+" hook relay "+ev || h.Timeout != 5 {
			t.Fatalf("%s hook %+v", ev, h)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	// windows reports 0666 or 0444 for every file (no unix permission bits), so
	// the 0600 contract is only observable on unix hosts.
	if goruntime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestRenderAgentHookPackMergesRelayEntries(t *testing.T) {
	dir := t.TempDir()
	existing := `{"permissions":{"allow":["Read"]},"hooks":{
  "SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"keep session start","timeout":5}]}],
  "PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"keep pre one","timeout":7}]}],
  "PostToolUse":[{"matcher":"","hooks":[{"type":"command","command":"keep post","timeout":5}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "cascade")
	if err := RenderAgentHookPack(dir, exe); err != nil {
		t.Fatal(err)
	}
	doc, hooks := readPack(t, dir)
	var perms map[string][]string
	if err := json.Unmarshal(doc["permissions"], &perms); err != nil || !reflect.DeepEqual(perms, map[string][]string{"allow": {"Read"}}) {
		t.Fatalf("a top-level key was lost or rewritten: %s (%v)", doc["permissions"], err)
	}
	if got := hooks["SessionStart"]; len(got) != 1 || got[0].Hooks[0].Command != "keep session start" {
		t.Fatalf("SessionStart changed: %+v", got)
	}
	for ev, keep := range map[string]string{"PreToolUse": "keep pre one", "PostToolUse": "keep post"} {
		entries := hooks[ev]
		if len(entries) != 2 || entries[0].Hooks[0].Command != keep || entries[0].Hooks[0].Timeout == 0 {
			t.Fatalf("%s: existing entry not kept first: %+v", ev, entries)
		}
		if relayCommands(entries, " hook relay "+ev) != 1 {
			t.Fatalf("%s: relay entry missing or duplicated: %+v", ev, entries)
		}
	}
	if len(hooks["Stop"]) != 1 {
		t.Fatalf("Stop: %+v", hooks["Stop"])
	}
	first, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err := RenderAgentHookPack(dir, exe); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(first) != string(second) {
		t.Fatal("rendering twice changed the pack")
	}
}

func TestRenderAgentHookPackRefusals(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "cascade")
	for name, bad := range map[string]string{"empty": "", "relative": "cascade", "quote": exe + `"; rm -rf ~; "`,
		"dollar": exe + "$HOME", "backtick": exe + "`x`", "newline": exe + "\nx", "semicolon": exe + ";x",
		"trailing backslash": exe + `\`} {
		t.Run("exe "+name, func(t *testing.T) {
			dir := t.TempDir()
			if err := RenderAgentHookPack(dir, bad); kindOf(err) != cascade.KindInvalidInput {
				t.Fatalf("got %v, want an invalid-input refusal", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
				t.Fatal("a refused render wrote settings.json")
			}
		})
	}
	t.Run("settings is not an object", func(t *testing.T) {
		dir := t.TempDir()
		for _, body := range []string{`[1]`, `"x"`, `{"hooks":[1]}`, `{"hooks":"x"}`} {
			_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600)
			if err := RenderAgentHookPack(dir, exe); kindOf(err) != cascade.KindInvalidInput {
				t.Fatalf("%s: got %v", body, err)
			}
		}
	})
	t.Run("a path with spaces is quoted", func(t *testing.T) {
		dir, spaced := t.TempDir(), filepath.Join(t.TempDir(), "my tools", "cascade")
		if err := RenderAgentHookPack(dir, spaced); err != nil {
			t.Fatal(err)
		}
		if _, hooks := readPack(t, dir); hooks["Stop"][0].Hooks[0].Command != `"`+spaced+`" hook relay Stop` {
			t.Fatalf("got %q", hooks["Stop"][0].Hooks[0].Command)
		}
	})
}

// TestRelayExeBackslashShapes covers both platforms' rules on one host: off
// Windows any backslash is refused (sh reads \" as an escaped quote, a syntax
// error that exits 2 and blocks the tool call); on Windows only a backslash
// directly before the closing quote is.
func TestRelayExeBackslashShapes(t *testing.T) {
	cases := []struct {
		goos, exe string
		want      bool
	}{
		{"linux", "/opt/tools/cascade", true},
		{"linux", `/opt/foo\`, false},
		{"darwin", `/opt/a\b/cascade`, false},
		{"windows", `C:\Program Files\cascade\cascade.exe`, true},
		{"windows", `C:\tools\cascade\`, false},
	}
	for _, c := range cases {
		if got := exeShellSafe(c.exe, c.goos); got != c.want {
			t.Errorf("exeShellSafe(%q, %s) = %t, want %t", c.exe, c.goos, got, c.want)
		}
	}
}

func TestRenderAgentHookPackRefusesSymlinks(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "cascade")
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := RenderAgentHookPack(filepath.Join(base, "link"), exe); kindOf(err) != cascade.KindCapabilityDenied {
		t.Fatalf("a symlinked pack directory: %v", err)
	}
	dir := t.TempDir()
	target := filepath.Join(base, "elsewhere.json")
	_ = os.WriteFile(target, []byte(`{}`), 0o600)
	if err := os.Symlink(target, filepath.Join(dir, "settings.json")); err != nil {
		t.Skip(err)
	}
	if err := RenderAgentHookPack(dir, exe); kindOf(err) != cascade.KindCapabilityDenied {
		t.Fatalf("a symlinked settings.json: got %v, want a capability-denied refusal", err)
	}
	if b, _ := os.ReadFile(target); !reflect.DeepEqual(string(b), `{}`) {
		t.Fatalf("the render wrote through the symlink: %s", b)
	}
}
