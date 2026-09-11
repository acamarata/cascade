package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withGenerator(t *testing.T, g GeneratorFunc) {
	t.Helper()
	prev := Generate
	Generate = g
	t.Cleanup(func() { Generate = prev })
}

func TestUnwiredGeneratorRefuses(t *testing.T) {
	prev := Generate
	Generate = unwiredGenerator
	defer func() { Generate = prev }()

	if _, err := Install(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Install with unwired generator: want error, got nil")
	}
}

func TestSetGeneratorRejectsNil(t *testing.T) {
	prev := Generate
	defer func() { Generate = prev }()

	if err := SetGenerator(nil); err == nil {
		t.Fatal("SetGenerator(nil): want error, got nil")
	}
	// Generate must be unchanged by the rejected call.
	if _, err := Generate(context.Background(), "x"); err == nil {
		t.Fatal("Generate: want unwiredGenerator's error to survive a rejected SetGenerator(nil)")
	}
}

func TestSetGeneratorInstalls(t *testing.T) {
	prev := Generate
	defer func() { Generate = prev }()

	called := false
	if err := SetGenerator(func(context.Context, string) ([]GeneratedFile, error) {
		called = true
		return nil, nil
	}); err != nil {
		t.Fatalf("SetGenerator: %v", err)
	}
	if _, err := Generate(context.Background(), "x"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !called {
		t.Error("SetGenerator did not install the given generator")
	}
}

// TestCascadeCodexInstallIdempotent proves a second Install run over
// identical generator output writes nothing and reports Changed=false for
// every file, while the first run creates the file and reports
// Changed=true.
func TestCascadeCodexInstallIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	content := []byte("cascade fixture content\n")

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: content}}, nil
	})

	first, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if len(first) != 1 || !first[0].Changed {
		t.Fatalf("first Install = %+v, want one Changed=true result", first)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(content) {
		t.Fatalf("file content = %q, want %q", got, content)
	}

	second, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(second) != 1 || second[0].Changed {
		t.Fatalf("second Install = %+v, want one Changed=false result (idempotent)", second)
	}
}

func TestInstallChangedContentRewrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("v1\n")}}, nil
	})
	if _, err := Install(context.Background(), dir); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("v2\n")}}, nil
	})
	results, err := Install(context.Background(), dir)
	if err != nil {
		t.Fatalf("Install v2: %v", err)
	}
	if len(results) != 1 || !results[0].Changed {
		t.Fatalf("Install v2 = %+v, want Changed=true", results)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2\n" {
		t.Fatalf("file content = %q, want %q", got, "v2\n")
	}
}

func TestInstallGeneratorError(t *testing.T) {
	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return nil, errors.New("boom")
	})
	if _, err := Install(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Install: want error when generator fails, got nil")
	}
}

func TestInstallWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// A path under a file (not a directory) can never be created.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	badPath := filepath.Join(blocker, "AGENTS.md")

	withGenerator(t, func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: badPath, Content: []byte("x")}}, nil
	})
	if _, err := Install(context.Background(), dir); err == nil {
		t.Fatal("Install: want error when the target directory cannot be created, got nil")
	}
}

func TestRunInstallUsesRealCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	withGenerator(t, func(_ context.Context, _ string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("cwd-driven\n")}}, nil
	})

	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prevWd) }()

	if err := runInstall(context.Background(), nil); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}
