package build

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose (this file): hold exemption 4 (a `package main` file under
//   plugins/**) to its stated width. An exemption nobody bounds is how a
//   gate stops gating, so each edge is asserted with a seeded file rather
//   than by reading the predicate.
// SPORT: internal/build tests (ADD) — P1-E25-W5-S51-T1.

// TestPluginEntryPointExemptionEdges asserts the predicate directly, on
// every combination of the two conditions that define it.
func TestPluginEntryPointExemptionEdges(t *testing.T) {
	for _, tc := range []struct {
		path, pkg string
		want      bool
		why       string
	}{
		{"plugins/github/main.go", "main", true,
			"a process-tier plugin's entry point is a standalone binary"},
		{"/abs/repo/plugins/github/main.go", "main", true,
			"an absolute path names the same file"},
		{"plugins/github/auth_flow.go", "main", true,
			"a second file of the same main package is part of that binary"},
		{"plugins/github/tools/client.go", "tools", false,
			"a plugin's LIBRARY code has no business naming a stream"},
		{"plugins/claude/plugin.go", "claude", false,
			"a builtin plugin is linked into the CLI and stays gated"},
		{"cmd/cascade/main.go", "main", false,
			"the CLI is exactly what this gate protects"},
		{"internal/daemon/main.go", "main", false,
			"a main package outside plugins/ is not a plugin binary"},
	} {
		if got := OutputgateIsPluginEntryPoint(tc.path, tc.pkg); got != tc.want {
			t.Errorf("OutputgateIsPluginEntryPoint(%q, %q) = %v, want %v — %s",
				tc.path, tc.pkg, got, tc.want, tc.why)
		}
	}
}

// TestPluginLibraryFileIsStillScanned is the seeded proof that the
// exemption did not quietly widen to the whole plugins tree: a non-main
// file carrying the denied selector must still be reported.
func TestPluginLibraryFileIsStillScanned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugins", "seeded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "library.go")
	source := "package seeded\n\nimport \"os\"\n\nvar sink = os.Stdout\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	found, err := OutputgateScanFile(path)
	if err != nil {
		t.Fatalf("OutputgateScanFile: %v", err)
	}
	if len(found) != 1 || found[0].Call != "os.Stdout" {
		t.Fatalf("a plugin LIBRARY file naming os.Stdout was not reported: %v", found)
	}
}

// TestPluginEntryPointIsNotScanned is the other half of the same seeded
// proof: the identical source, in a main package, is exempt.
func TestPluginEntryPointIsNotScanned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugins", "seeded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main.go")
	source := "package main\n\nimport \"os\"\n\nvar sink = os.Stdout\n\nfunc main() { _ = sink }\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	found, err := OutputgateScanFile(path)
	if err != nil {
		t.Fatalf("OutputgateScanFile: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a plugin entry point was reported despite exemption 4: %v", found)
	}
}
