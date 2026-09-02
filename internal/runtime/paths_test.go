package runtime

import (
	"errors"
	"path/filepath"
	"testing"
)

// fakeHomeDir returns a HomeDirFunc rooted at t.TempDir() (Art.7.1: tests
// never touch the real user home).
func fakeHomeDir(t *testing.T) HomeDirFunc {
	t.Helper()
	dir := t.TempDir()
	return func() (string, error) { return dir, nil }
}

func TestNewPathProvider_DefaultDerivation(t *testing.T) {
	home := t.TempDir()
	getenv := func(string) string { return "" }
	p, err := NewPathProvider(getenv, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	wantRoot := filepath.Join(home, ".cascade")
	if p.Root() != wantRoot {
		t.Errorf("Root() = %q, want %q", p.Root(), wantRoot)
	}
	if p.ConfigPath() != filepath.Join(wantRoot, "config.toml") {
		t.Errorf("ConfigPath() = %q", p.ConfigPath())
	}
	if p.SocketPath() != filepath.Join(wantRoot, "daemon.sock") {
		t.Errorf("SocketPath() = %q, want derived default", p.SocketPath())
	}
	if p.DataDir() != filepath.Join(wantRoot, "data") {
		t.Errorf("DataDir() = %q", p.DataDir())
	}
	if p.LogDir() != filepath.Join(wantRoot, "logs") {
		t.Errorf("LogDir() = %q", p.LogDir())
	}
}

func TestNewPathProvider_CascadeHomeOverride(t *testing.T) {
	root := t.TempDir()
	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return root
		}
		return ""
	}
	p, err := NewPathProvider(getenv, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	if p.Root() != root {
		t.Errorf("Root() = %q, want %q", p.Root(), root)
	}
}

// TestNewPathProvider_CascadeSocketOverride is the R-14.94 test: the
// CASCADE_SOCKET env override resolves ahead of the derived default.
func TestNewPathProvider_CascadeSocketOverride(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom.sock")
	getenv := func(k string) string {
		switch k {
		case "CASCADE_HOME":
			return root
		case "CASCADE_SOCKET":
			return override
		default:
			return ""
		}
	}
	p, err := NewPathProvider(getenv, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	if p.SocketPath() != override {
		t.Errorf("SocketPath() = %q, want override %q", p.SocketPath(), override)
	}
}

func TestNewPathProvider_CascadeConfigOverride(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom-config.toml")
	getenv := func(k string) string {
		switch k {
		case "CASCADE_HOME":
			return root
		case "CASCADE_CONFIG":
			return override
		default:
			return ""
		}
	}
	p, err := NewPathProvider(getenv, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	if p.ConfigPath() != override {
		t.Errorf("ConfigPath() = %q, want override %q", p.ConfigPath(), override)
	}
}

func TestNewPathProvider_StorageRootPerProfile(t *testing.T) {
	root := t.TempDir()
	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return root
		}
		return ""
	}
	p, err := NewPathProvider(getenv, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	for _, prof := range []Profile{ProfileLocal, ProfileServer, ProfileWorker} {
		want := filepath.Join(p.DataDir(), "storage", string(prof))
		if got := p.StorageRoot(prof); got != want {
			t.Errorf("StorageRoot(%q) = %q, want %q", prof, got, want)
		}
	}
}

func TestNewPathProvider_HomeDirError(t *testing.T) {
	getenv := func(string) string { return "" }
	boom := errors.New("no home for you")
	_, err := NewPathProvider(getenv, func() (string, error) { return "", boom })
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want wrapping %v", err, boom)
	}
}

func TestNewDefaultPathProvider_Smoke(t *testing.T) {
	// Only asserts it constructs without error; NewDefaultPathProvider is
	// the one production call site allowed to touch the real environment
	// (Art.7.1 forbids test use beyond this smoke check), so this test
	// does not assert on the resulting paths.
	if _, err := NewDefaultPathProvider(); err != nil {
		t.Fatalf("NewDefaultPathProvider: %v", err)
	}
}
