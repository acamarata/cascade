// Package plugintest is a test helper for process-plugin tests: it builds
// the real cascade-github executable and installs a trusted record through
// the real grant path.
// Inputs: a testing.TB, a plugin store, a plugin id and manifest bytes.
// Outputs: BuildGithub (an executable directory) and InstallTrusted (a
// PluginRecordRef to the record the grant wrote).
// Constraints: callers are tests. A trusted record comes only from a
// plugin.add dispatched through rpc.ElevationMiddleware over an injected
// platform-tier test custody, answered with a signature from a fresh test
// key, then InstallMiddleware and ProvisionElevated; nothing here writes
// TrustTier or ExecDigest. Everything is written under t.TempDir() and the
// build never reaches the network.
// SPORT: internal/plugins/plugintest (P1-PLG-06).
package plugintest

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver ProvisionElevated's domain claim uses

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// GithubImportPath is the module import path of the cascade-github plugin.
const GithubImportPath = "github.com/acamarata/cascade/plugins/github"

// GithubID is the cascade-github plugin id and executable name.
const GithubID = "cascade-github"

var githubBuild struct {
	once sync.Once
	bin  []byte
	err  error
	log  []byte
}

// ExecutableName is id's executable file name on this platform.
func ExecutableName(id string) string {
	if goruntime.GOOS == "windows" {
		return id + ".exe"
	}
	return id
}

// BuildGithub compiles cascade-github once per process and writes the
// binary into a fresh t.TempDir(), which it returns: the directory to hand
// to plugins.SetExecutableDirForTest.
func BuildGithub(t testing.TB) (dir string) {
	t.Helper()
	githubBuild.once.Do(func() {
		out := filepath.Join(t.TempDir(), ExecutableName(GithubID))
		cmd := exec.Command("go", "build", "-o", out, GithubImportPath)
		cmd.Env = buildEnv()
		if githubBuild.log, githubBuild.err = cmd.CombinedOutput(); githubBuild.err == nil {
			githubBuild.bin, githubBuild.err = os.ReadFile(out)
		}
	})
	if githubBuild.err != nil {
		t.Fatalf("go build %s: %v\n%s", GithubImportPath, githubBuild.err, githubBuild.log)
	}
	dir = t.TempDir()
	path := filepath.Join(dir, ExecutableName(GithubID))
	if err := runtime.WriteFileAtomic(path, githubBuild.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// goToolEnv is the go tool's cache locations, resolved when the test
// binary starts and before any test redirects HOME, so a build under a
// redirected HOME reuses the existing module and build caches.
var goToolEnv = func() []string {
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE", "GOPATH").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return nil
	}
	return []string{"GOMODCACHE=" + lines[0], "GOCACHE=" + lines[1], "GOPATH=" + lines[2]}
}()

// buildEnv is the build's environment: the process environment, the
// pinned cache locations, and no module proxy, so the build never reaches
// the network.
func buildEnv() []string {
	return append(append(os.Environ(), goToolEnv...), "GOPROXY=off", "GOFLAGS=-mod=readonly")
}

// PluginRecordRef names the record InstallTrusted wrote.
type PluginRecordRef struct {
	Store provider.Store
	ID    string
}

// Load returns the referenced record, failing the test when it is absent.
func (r PluginRecordRef) Load(t testing.TB) plugins.PluginMetadata {
	t.Helper()
	rec, ok, err := plugins.LoadMetadata(context.Background(), r.Store, r.ID)
	if err != nil || !ok {
		t.Fatalf("loading %s's record: ok=%v err=%v", r.ID, ok, err)
	}
	return rec
}

// InstallTrusted installs id from manifest into store through a witnessed
// plugin.add (see the package comment). The executable directory must
// already hold id's executable. It fails the test unless the grant
// succeeded.
func InstallTrusted(t testing.TB, store provider.Store, id string, manifest []byte) PluginRecordRef {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("Windows tier-2 refuses elevation before the attestation flow")
	}
	raw := addParams(t, id, manifest)
	handler := addChain(store, openDomainDB(t))
	if err := attestedDispatch(t, raw, handler); err != nil {
		t.Fatalf("witnessed plugin.add of %s: %v", id, err)
	}
	return PluginRecordRef{Store: store, ID: id}
}

// openDomainDB opens a plugin domain database under t.TempDir().
func openDomainDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plugin-domains.db"))
	if err != nil {
		t.Fatalf("opening the plugin domain database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// fixedClock is the fixed clock of the domain claim and the elevation ledger.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(4000, 0).UTC() }
