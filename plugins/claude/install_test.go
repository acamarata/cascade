package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// instructionFileName is the basename the harness instruction writer
// emits, used here to build realistic generator output. It is a test
// constant, not a production one: this plugin writes whatever paths the
// injected generator hands it and never names the file itself.
const instructionFileName = "CLAUDE.md"

// envMap builds an Env over a fixed map, so every platform branch of
// ResolvePaths runs on any host without mutating the real process
// environment (the contract's "no platform path is discovered by probing").
func envMap(kv map[string]string) Env {
	return func(k string) string { return kv[k] }
}

// TestResolvePaths_PerPlatform pins the paths against the harness's real
// layout, verified against an installation (R-14.253).
//
// Every platform lands on $HOME/.claude, and the darwin case is the one
// that matters most: the earlier contract sent it to the macOS
// Application Support directory, which belongs to the DESKTOP application
// -- a different product sharing a name. Everything this plugin wrote
// there was invisible to the harness, and a wire that registered nothing
// still reported success.
func TestResolvePaths_PerPlatform(t *testing.T) {
	cases := []struct {
		name         string
		goos         string
		env          map[string]string
		wantRoot     string
		wantMCP      string
		wantSettings string
	}{
		{
			name:         "darwin is not the desktop application's directory",
			goos:         "darwin",
			env:          map[string]string{"HOME": "/Users/x"},
			wantRoot:     filepath.Join("/Users/x", ".claude"),
			wantMCP:      filepath.Join("/Users/x", ".claude.json"),
			wantSettings: filepath.Join("/Users/x", ".claude", "settings.json"),
		},
		{
			name:         "linux does not apply XDG to a harness that ignores it",
			goos:         "linux",
			env:          map[string]string{"XDG_CONFIG_HOME": "/cfg", "HOME": "/home/x"},
			wantRoot:     filepath.Join("/home/x", ".claude"),
			wantMCP:      filepath.Join("/home/x", ".claude.json"),
			wantSettings: filepath.Join("/home/x", ".claude", "settings.json"),
		},
		{
			name:         "windows reads the variable os.UserHomeDir reads there",
			goos:         "windows",
			env:          map[string]string{"USERPROFILE": `C:\Users\x`},
			wantRoot:     filepath.Join(`C:\Users\x`, ".claude"),
			wantMCP:      filepath.Join(`C:\Users\x`, ".claude.json"),
			wantSettings: filepath.Join(`C:\Users\x`, ".claude", "settings.json"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolvePaths(tc.goos, envMap(tc.env))
			if err != nil {
				t.Fatalf("ResolvePaths(%s): %v", tc.goos, err)
			}
			if got.ConfigRoot != tc.wantRoot {
				t.Fatalf("ConfigRoot = %q, want %q", got.ConfigRoot, tc.wantRoot)
			}
			if got.MCPConfig != tc.wantMCP {
				t.Fatalf("MCPConfig = %q, want %q", got.MCPConfig, tc.wantMCP)
			}
			if got.Settings != tc.wantSettings {
				t.Fatalf("Settings = %q, want %q", got.Settings, tc.wantSettings)
			}
		})
	}
}

// TestTheOverrideMovesTheUserConfigToo: an instance running under the
// config-dir override keeps its user config INSIDE that directory, while
// an instance without one keeps it beside HOME. Verified on a machine
// running both at once. Following only one of the two rules writes the
// MCP entry where one of the two instances never looks.
func TestTheOverrideMovesTheUserConfigToo(t *testing.T) {
	relocated := filepath.Join("/elsewhere", "cfg")
	got, err := ResolvePaths("darwin", envMap(map[string]string{
		"HOME": "/Users/x", OverrideVar(): relocated,
	}))
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if got.ConfigRoot != relocated {
		t.Errorf("ConfigRoot = %q, want the override %q", got.ConfigRoot, relocated)
	}
	if want := filepath.Join(relocated, ".claude.json"); got.MCPConfig != want {
		t.Errorf("MCPConfig = %q, want %q -- an overridden instance keeps its user config beside its own root",
			got.MCPConfig, want)
	}
	if want := filepath.Join(relocated, "settings.json"); got.Settings != want {
		t.Errorf("Settings = %q, want %q", got.Settings, want)
	}
}

// TestOverrideVarIsDerivedFromTheHarnessName keeps the deny-listed literal
// out of the tree while still proving the variable this plugin reads is
// the one the harness publishes.
func TestOverrideVarIsDerivedFromTheHarnessName(t *testing.T) {
	if got, want := OverrideVar(), strings.ToUpper(harnessName)+"_CONFIG_DIR"; got != want {
		t.Errorf("OverrideVar() = %q, want %q", got, want)
	}
}

// TestResolvePaths_MissingVariablesRefuse proves an unset variable is an
// error naming that variable, never a silent fallback to some path that
// happens to exist on the host.
func TestResolvePaths_MissingVariablesRefuse(t *testing.T) {
	cases := []struct {
		goos, wantIn string
	}{
		{"darwin", "HOME"},
		{"windows", "USERPROFILE"},
		{"linux", "HOME"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			_, err := ResolvePaths(tc.goos, envMap(nil))
			if err == nil {
				t.Fatalf("ResolvePaths(%s) with an empty environment succeeded, want a refusal", tc.goos)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("error = %v, want it to name %s", err, tc.wantIn)
			}
		})
	}
	if _, err := ResolvePaths("darwin", nil); err == nil {
		t.Fatal("ResolvePaths with a nil Env succeeded, want a refusal")
	}
}

// withGenerator swaps Generate for the duration of a test.
func withGenerator(t *testing.T, g GeneratorFunc) {
	t.Helper()
	prev := Generate
	Generate = g
	t.Cleanup(func() { Generate = prev })
}

// TestCascadeClaudeInstallIdempotent is the contract's named idempotency
// check: a second install writes nothing, reports no change, and does not
// disturb the file's modification time.
func TestCascadeClaudeInstallIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tier", instructionFileName)
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("# instructions\n")}}, nil
	})

	first, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(first) != 1 || !first[0].Changed {
		t.Fatalf("first install = %+v, want exactly one Changed result", first)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(second) != 1 || second[0].Changed {
		t.Fatalf("second install = %+v, want exactly one unchanged result", second)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("second install rewrote the file: mtime %v -> %v", before.ModTime(), after.ModTime())
	}
}

// TestInstallRewritesChangedContent proves the idempotency check above is a
// content comparison and not simply "the file exists": a changed golden
// must still land.
func TestInstallRewritesChangedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, instructionFileName)
	content := []byte("first\n")
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: content}}, nil
	})
	if _, err := Install(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	content = []byte("second\n")
	res, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Changed {
		t.Fatal("install reported no change after the generated content changed")
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Fatalf("on-disk content = %q, want %q", got, "second\n")
	}
}

// TestUnwiredGeneratorIsARealError proves the default seam fails loudly
// rather than installing nothing silently.
func TestUnwiredGeneratorIsARealError(t *testing.T) {
	withGenerator(t, unwiredGenerator)
	_, err := Install(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("Install with an unwired generator succeeded, want a real error")
	}
	if !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("error = %v, want it to say the generator is not wired", err)
	}
}

func TestSetGeneratorRefusesNil(t *testing.T) {
	if err := SetGenerator(nil); err == nil {
		t.Fatal("SetGenerator(nil) succeeded, want a refusal")
	}
}

// TestInstallPropagatesGeneratorError proves a generator failure is
// reported, never swallowed into an empty-but-successful install.
func TestInstallPropagatesGeneratorError(t *testing.T) {
	sentinel := errors.New("generator exploded")
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) { return nil, sentinel })
	if _, err := Install(context.Background(), t.TempDir()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the generator's own error", err)
	}
}
