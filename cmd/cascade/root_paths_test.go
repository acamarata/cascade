// Purpose: unit tests for root.go's lazyPaths — the deferred
//
//	runtime.PathProvider adapter mountConfigCmd/productionDaemonDeps hand to
//	the config/daemon command trees. Exercises both the happy path (a
//	resolvable CASCADE_HOME) and the failure path (home directory
//	resolution fails), since lazyPaths swallows that failure into an empty
//	string rather than propagating an error (root.go's own doc comment:
//	"returns the zero value if resolution fails").
//
// Constraints: Art.7.1 — every case roots CASCADE_HOME at a t.TempDir();
//
//	the failure case blanks HOME (and USERPROFILE, for a Windows CI lane)
//	via t.Setenv rather than touching the real environment permanently.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestLazyPaths_ResolvedHome asserts every accessor derives its path from
// CASCADE_HOME exactly as runtime.PathProvider documents: DataDir/LogDir
// are Root() joined with a fixed segment, and StorageRoot is DataDir()
// joined with "storage" and the profile name.
func TestLazyPaths_ResolvedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("CASCADE_CONFIG", "")
	t.Setenv("CASCADE_SOCKET", "")

	p := lazyPaths{}

	if got, want := p.Root(), home; got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}
	if got, want := p.ConfigPath(), filepath.Join(home, "config.toml"); got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
	if got, want := p.SocketPath(), filepath.Join(home, "daemon.sock"); got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := p.DataDir(), filepath.Join(home, "data"); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
	if got, want := p.LogDir(), filepath.Join(home, "logs"); got != want {
		t.Errorf("LogDir() = %q, want %q", got, want)
	}

	cases := []runtime.Profile{"local", "work", ""}
	for _, prof := range cases {
		got := p.StorageRoot(prof)
		want := filepath.Join(home, "data", "storage", string(prof))
		if got != want {
			t.Errorf("StorageRoot(%q) = %q, want %q", prof, got, want)
		}
	}
}

// TestLazyPaths_EnvOverridesWin asserts CASCADE_CONFIG/CASCADE_SOCKET, when
// set, are used verbatim instead of the Root()-derived default — the same
// precedence runtime.NewPathProvider documents.
func TestLazyPaths_EnvOverridesWin(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "custom-config.toml")
	sock := filepath.Join(home, "custom.sock")
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("CASCADE_CONFIG", cfg)
	t.Setenv("CASCADE_SOCKET", sock)

	p := lazyPaths{}
	if got := p.ConfigPath(); got != cfg {
		t.Errorf("ConfigPath() = %q, want override %q", got, cfg)
	}
	if got := p.SocketPath(); got != sock {
		t.Errorf("SocketPath() = %q, want override %q", got, sock)
	}
}

// TestLazyPaths_HomeResolutionFails asserts every accessor degrades to the
// empty string, rather than panicking or returning a stale value, when
// CASCADE_HOME is unset and the OS home directory cannot be resolved.
func TestLazyPaths_HomeResolutionFails(t *testing.T) {
	t.Setenv("CASCADE_HOME", "")
	// os.UserHomeDir reads $HOME on unix and %USERPROFILE% (or the
	// HOMEDRIVE+HOMEPATH pair) on Windows; blanking every one of them is
	// what makes resolution fail on every platform this repo builds for.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	p := lazyPaths{}
	if got := p.resolve(); got != nil {
		t.Fatalf("resolve() = %v, want nil when home resolution fails", got)
	}

	for name, got := range map[string]string{
		"Root":       p.Root(),
		"ConfigPath": p.ConfigPath(),
		"SocketPath": p.SocketPath(),
		"DataDir":    p.DataDir(),
		"LogDir":     p.LogDir(),
	} {
		if got != "" {
			t.Errorf("%s() = %q, want empty string when home resolution fails", name, got)
		}
	}
	if got := p.StorageRoot("local"); got != "" {
		t.Errorf("StorageRoot() = %q, want empty string when home resolution fails", got)
	}
}
