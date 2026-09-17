package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real-environment half of install.go's tests: HostPaths and the two
// command handlers. Split out of install_test.go to stay under the
// 300-line file cap, which counts test files too.
// hostEnvFixture points HOME (and XDG_CONFIG_HOME) at a temp directory so
// the real HostPaths resolver lands entirely inside the test's own tree,
// and returns the paths it will resolve to.
func hostEnvFixture(t *testing.T) Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	paths, err := HostPaths()
	if err != nil {
		t.Skipf("HostPaths is refused on this platform: %v", err)
	}
	return paths
}

// TestHostPathsUsesTheRealEnvironment covers the production entry point,
// not only the injectable core: a resolver that is only ever called with a
// fake Env is a resolver whose real wiring is never exercised.
func TestHostPathsUsesTheRealEnvironment(t *testing.T) {
	if runtimeIsWindows() {
		if _, err := HostPaths(); err == nil {
			t.Fatal("HostPaths succeeded on Windows, want the tier-2 refusal")
		}
		return
	}
	paths := hostEnvFixture(t)
	home := os.Getenv("HOME")
	if !strings.HasPrefix(paths.ConfigRoot, home) {
		t.Fatalf("ConfigRoot = %q, want it under the HOME the test set (%q)", paths.ConfigRoot, home)
	}
	// A sibling of the config root, not a file inside it: the user-scope
	// MCP config hangs off HOME directly.
	if paths.MCPConfig != filepath.Join(home, mcpConfigName) {
		t.Fatalf("MCPConfig = %q, want the user-scope file under HOME (%q)",
			paths.MCPConfig, filepath.Join(home, mcpConfigName))
	}
}

// TestRunInstallInstallsEverything drives the real command handler, which
// is the path `cascade init` step 6 reaches: instructions, hook pack and
// MCP entry all land in one call.
func TestRunInstallInstallsEverything(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("harness install is refused on Windows tier-2; TestHostPathsRefusesWindows covers that branch")
	}
	paths := hostEnvFixture(t)
	instruction := filepath.Join(t.TempDir(), instructionFileName)
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: instruction, Content: []byte("# instructions\n")}}, nil
	})
	withRenderer(t, rendersSocket())
	withLookPath(t, resolvesTo("/usr/local/bin/cascade"))
	t.Setenv("CASCADE_SOCKET", "/run/cascade.sock")

	if err := runInstall(context.Background(), nil); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	for _, path := range []string{
		instruction,
		filepath.Join(paths.HookConfig, hookPackFile),
		paths.MCPConfig,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("runInstall did not produce %s: %v", path, err)
		}
	}
}

// TestRunInstallPropagatesAMissingSocket proves the command handler
// surfaces the hook-pack step's refusal rather than reporting success after
// installing only the instructions.
func TestRunInstallPropagatesAMissingSocket(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("harness install is refused on Windows tier-2 before the socket is read")
	}
	hostEnvFixture(t)
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) { return nil, nil })
	withRenderer(t, rendersSocket())
	t.Setenv("CASCADE_SOCKET", "")

	err := runInstall(context.Background(), nil)
	if err == nil {
		t.Fatal("runInstall succeeded with CASCADE_SOCKET unset, want the hook-pack refusal")
	}
	if !strings.Contains(err.Error(), "CASCADE_SOCKET") {
		t.Fatalf("error = %v, want it to name the missing variable", err)
	}
}

// TestWriteAtomicRefusesAnUnusableDirectory covers writeAtomic's failure
// branch: a parent path that is a regular file cannot hold the temp file,
// and that must surface as an error rather than a silent skip.
func TestWriteAtomicRefusesAnUnusableDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil { //nolint:gosec // test-local temp path.
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(blocker, "child", "file.md"), []byte("y")); err == nil {
		t.Fatal("writeAtomic succeeded through a regular file, want an error")
	}
}

// TestWriteIfChangedReportsAReadFailure covers the non-IsNotExist read
// branch, so an unreadable existing file is reported instead of being
// treated as absent and silently overwritten.
func TestWriteIfChangedReportsAReadFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeIfChanged(dir, []byte("content")); err == nil {
		t.Fatal("writeIfChanged succeeded against a directory, want an error")
	}
}

func TestSetGeneratorAcceptsARealGenerator(t *testing.T) {
	prev := Generate
	t.Cleanup(func() { Generate = prev })
	if err := SetGenerator(func(context.Context, string) ([]GeneratedFile, error) { return nil, nil }); err != nil {
		t.Fatalf("SetGenerator: %v", err)
	}
}
