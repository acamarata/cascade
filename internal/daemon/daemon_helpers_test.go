package daemon

// Purpose: tiny shared test helpers (a fake PathProvider and a raw-string
//   file writer for corrupt-file fixtures) used by daemon_test.go and the
//   unix-only lifecycle tests. Split out under R-14.117/R-14.133 (a helper
//   file the language forces into existence to avoid every test file
//   redefining the same fake) — same package, no behaviour of its own.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// fakePaths is a minimal runtime.PathProvider for tests that only need a
// couple of the interface's methods to return fixed values.
type fakePaths struct {
	root       string
	socketPath string
}

func (p fakePaths) Root() string       { return p.root }
func (p fakePaths) ConfigPath() string { return p.root + "/config.toml" }
func (p fakePaths) SocketPath() string { return p.socketPath }
func (p fakePaths) DataDir() string    { return p.root + "/data" }
func (p fakePaths) LogDir() string     { return p.root + "/logs" }
func (p fakePaths) StorageRoot(prof runtime.Profile) string {
	return p.root + "/data/storage/" + string(prof)
}

// fakePathsFor builds a fakePaths rooted at t.TempDir() (Art.7.1: tests
// never touch the real home directory) reporting socketPath as its
// SocketPath.
func fakePathsFor(t *testing.T, socketPath string) runtime.PathProvider {
	t.Helper()
	return fakePaths{root: t.TempDir(), socketPath: socketPath}
}

// writeFileHelper writes s to path as a plain file (0600), for corrupt/
// fixture-file tests that need content readPIDFile cannot parse.
func writeFileHelper(path, s string) error {
	return os.WriteFile(path, []byte(s), 0o600)
}

// shortTempDir returns a temp directory NOT rooted under t.TempDir(): the
// latter embeds the full (potentially long) test name in its path, which a
// unix domain socket's sockaddr_un.sun_path (~104 bytes on Darwin) can
// overflow on its own before a single filename is even appended. Tests
// that need a real unix socket path use this instead; t.Cleanup removes it
// the same way t.TempDir()'s own cleanup would.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascd")
	if err != nil {
		t.Fatalf("shortTempDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
