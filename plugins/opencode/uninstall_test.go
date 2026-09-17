package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCascadeOpencodeUninstallHook proves the full install-then-uninstall
// cycle: install creates the file, uninstall removes exactly it, and a
// second uninstall on the now-absent file reports AlreadyClean rather than
// erroring (idempotent in both directions, per the ticket's rule 9).
func TestCascadeOpencodeUninstallHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("hi\n")}}, nil
	})

	if _, err := Install(context.Background(), dir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: expected %s to exist: %v", path, err)
	}

	first, err := Uninstall(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("first Uninstall: %v", err)
	}
	if len(first) != 1 || !first[0].Removed || first[0].AlreadyClean {
		t.Fatalf("first Uninstall = %+v, want one Removed=true result", first)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone after uninstall, stat err = %v", path, err)
	}

	second, err := Uninstall(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
	if len(second) != 1 || second[0].Removed || !second[0].AlreadyClean {
		t.Fatalf("second Uninstall = %+v, want one AlreadyClean=true result", second)
	}
}

// TestUninstallRefusesUnrecognizedPath proves uninstall cannot touch a
// path the plugin did not create: a generator returning a path whose base
// name is not the managed instruction file name is refused outright,
// never removed, regardless of whether such a file exists on disk.
func TestUninstallRefusesUnrecognizedPath(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "passwd")
	if err := os.WriteFile(decoy, []byte("root:x:0:0\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: decoy, Content: []byte("root:x:0:0\n")}}, nil
	})

	if _, err := Uninstall(context.Background(), dir, nil); err == nil {
		t.Fatal("Uninstall: want refusal error for a non-AGENTS.md path, got nil")
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Fatalf("decoy file must survive the refused uninstall, stat err = %v", err)
	}
}

func TestIsManagedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"/a/b/AGENTS.md", true},
		{"AGENTS.md", true},
		{"/a/b/agents.md", false},
		{"/a/b/AGENTS.md.bak", false},
		{"/a/AGENTS.md/extra", false},
	}
	for _, c := range cases {
		if got := isManagedPath(c.path); got != c.want {
			t.Errorf("isManagedPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestUninstallGeneratorError(t *testing.T) {
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return nil, errors.New("boom")
	})
	if _, err := Uninstall(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("Uninstall: want error when generator fails, got nil")
	}
}

func TestUninstallStatFailure(t *testing.T) {
	// Confirms the AlreadyClean branch handles a deeply nested absent
	// path (parent directories missing too) without error - the
	// realistic shape of "target is missing".
	dir := t.TempDir()
	nested := filepath.Join(dir, "does", "not", "exist", "AGENTS.md")
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: nested, Content: []byte("x")}}, nil
	})
	results, err := Uninstall(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(results) != 1 || !results[0].AlreadyClean {
		t.Fatalf("results = %+v, want AlreadyClean=true", results)
	}
}

// TestRemoveManagedStatError exercises removeManaged's non-IsNotExist stat
// failure branch: a path containing a NUL byte is rejected by the kernel
// with EINVAL, not ENOENT, so it must surface as a wrapped error rather
// than a false AlreadyClean.
func TestRemoveManagedStatError(t *testing.T) {
	bad := "/tmp/x\x00y/AGENTS.md"
	if _, err := removeManaged(bad, nil); err == nil {
		t.Fatal("removeManaged with a NUL-containing path: want error, got nil")
	}
}

func TestRunUninstallUsesRealCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("x")}}, nil
	})

	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prevWd) }()

	withSharedPathResolver(t, noSharedPaths)
	if err := runUninstall(context.Background(), nil); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s removed, stat err = %v", path, err)
	}
}

// withSharedPathResolver swaps the shared-path resolver for the duration
// of a test, the way withGenerator swaps the generator.
func withSharedPathResolver(t *testing.T, r SharedPathResolverFunc) {
	t.Helper()
	prev := ResolveSharedPaths
	if err := SetSharedPathResolver(r); err != nil {
		t.Fatalf("SetSharedPathResolver: %v", err)
	}
	t.Cleanup(func() { ResolveSharedPaths = prev })
}

// noSharedPaths is the resolver for a machine with no other harness
// installed: it answers an empty set, which is a different thing from the
// unwired default's refusal.
func noSharedPaths(context.Context, string) (SharedPaths, error) { return SharedPaths{}, nil }

// TestTheUnwiredResolverRefusesRatherThanAssumingNothingIsShared is the
// Article-1 assertion for this seam. "Nothing is shared" is the answer
// that deletes a file another harness was still reading, so a host that
// forgot to wire the resolver must be refused, not served that answer
// (R-14.265).
func TestTheUnwiredResolverRefusesRatherThanAssumingNothingIsShared(t *testing.T) {
	prev := ResolveSharedPaths
	ResolveSharedPaths = unwiredSharedPaths
	t.Cleanup(func() { ResolveSharedPaths = prev })

	if _, err := ResolveSharedPaths(context.Background(), t.TempDir()); err == nil {
		t.Fatal("the unwired resolver answered; it must refuse")
	}
	if err := SetSharedPathResolver(nil); err == nil {
		t.Fatal("SetSharedPathResolver(nil) succeeded, reverting to the unwired default silently")
	}
}

// TestUninstallKeepsAFileAnotherHarnessStillReads is the rule itself.
func TestUninstallKeepsAFileAnotherHarnessStillReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, agentsFileName)
	if err := os.WriteFile(path, []byte("# shared\n"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("# shared\n")}}, nil
	})

	results, err := Uninstall(context.Background(), dir, SharedPaths{path: "codex"})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(results) != 1 || !results[0].Kept {
		t.Fatalf("results = %+v, want the shared file kept", results)
	}
	if results[0].KeptReason == "" {
		t.Error("a kept file carries no reason; the operator is told a file survived and nothing else")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the file another harness reads was removed anyway: %v", statErr)
	}

	// The same call with nothing else installed removes it, so the keep is
	// about the other harness and not about the file's name.
	after, err := Uninstall(context.Background(), dir, SharedPaths{})
	if err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
	if len(after) != 1 || !after[0].Removed {
		t.Fatalf("results = %+v, want the file removed once nothing else reads it", after)
	}
}
