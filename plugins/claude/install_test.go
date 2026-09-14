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

func TestResolvePaths_PerPlatform(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		env      map[string]string
		wantRoot string
	}{
		{
			name:     "darwin under HOME",
			goos:     "darwin",
			env:      map[string]string{"HOME": "/Users/x"},
			wantRoot: filepath.Join("/Users/x", "Library", "Application Support", "Claude"),
		},
		{
			name:     "linux honours XDG_CONFIG_HOME",
			goos:     "linux",
			env:      map[string]string{"XDG_CONFIG_HOME": "/cfg", "HOME": "/home/x"},
			wantRoot: filepath.Join("/cfg", "claude"),
		},
		{
			name:     "linux falls back to HOME/.config",
			goos:     "linux",
			env:      map[string]string{"HOME": "/home/x"},
			wantRoot: filepath.Join("/home/x", ".config", "claude"),
		},
		{
			name:     "windows under APPDATA",
			goos:     "windows",
			env:      map[string]string{"APPDATA": `C:\Users\x\AppData\Roaming`},
			wantRoot: filepath.Join(`C:\Users\x\AppData\Roaming`, "Claude"),
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
			if want := filepath.Join(tc.wantRoot, "mcp.json"); got.MCPConfig != want {
				t.Fatalf("MCPConfig = %q, want %q", got.MCPConfig, want)
			}
			if want := filepath.Join(tc.wantRoot, "hooks"); got.HookConfig != want {
				t.Fatalf("HookConfig = %q, want %q", got.HookConfig, want)
			}
		})
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
		{"windows", "APPDATA"},
		{"linux", "XDG_CONFIG_HOME"},
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
