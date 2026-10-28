//go:build !windows

// Purpose: the trusted launch at the real composition roots: the real
// cascade-github executable starts only after an attested plugin.add on the
// elevated daemon registry; composeDaemon installs a launch environment over
// the daemon's own store and bus; cancelling the daemon's run context reaps
// every launched child and its grandchild.
// SPORT: cmd/cascade plugin launch tests (P1-PLG-03).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// goBuild compiles pkg (relative to this package) to dir/name.
func goBuild(t *testing.T, pkg, dir, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
	if err := os.Chmod(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// useTestLaunchEnv installs a launch source over store with its own effect
// log (the daemon's openEffectLog needs host custody) and runs recovery.
func useTestLaunchEnv(t *testing.T, store provider.Store) {
	t.Helper()
	el, err := audit.NewEffectLog(audit.New(store, runtime.NewSystemClock(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.RecoverPluginEffects(context.Background(), el, filepath.Join(t.TempDir(), "plugin-effects")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) {
		return plugins.LaunchEnv{Store: store, Effects: el, Registrar: noopPluginRegistrar{}, Getenv: func(string) string { return "" }}, nil
	}))
}

type noopPluginRegistrar struct{}

func (noopPluginRegistrar) Register(process.EgressClass, process.EgressRegistrationConfig) error {
	return nil
}

// attestedAdd installs id from manifest through an elevated plugin.add.
func attestedAdd(t *testing.T, call func(ctx context.Context, method string, params, out any) error, id, manifest string) {
	t.Helper()
	var out any
	p := pluginAddParams{ID: id, Checksum: hexSum([]byte(manifest)), ManifestBytes: []byte(manifest)}
	if err := call(context.Background(), "plugin.add", p, &out); err != nil {
		t.Fatalf("attested plugin.add %s: %v", id, err)
	}
}

func TestRealGithubLaunchesOnlyAfterAttestedAdd(t *testing.T) {
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, "cascade-github")
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true) // what newElevatedTestRegistry builds
	useTestLaunchEnv(t, d.Store)
	ctx := context.Background()
	if h, err := plugins.LaunchInstalled(ctx, "cascade-github"); h != nil || !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("LaunchInstalled before plugin.add = %v, %v; want KindNotFound", h, err)
	}
	manifest, err := os.ReadFile("../../plugins/github/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	attestedAdd(t, d.Call, "cascade-github", string(manifest))
	h, err := plugins.LaunchInstalled(ctx, "cascade-github")
	if err != nil {
		t.Fatalf("LaunchInstalled after an attested plugin.add: %v", err)
	}
	if _, err := h.Call(ctx, "cascade.hello", []byte(`{"min_protocol_version":"1.0.0"}`)); err != nil {
		t.Fatalf("cascade.hello on the live child: %v", err)
	}
	if err := h.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDaemonLaunchUsesDaemonStore(t *testing.T) {
	composeElevated(t, true)
	wired := pluginLaunchWired.Load()
	env, err := plugins.ResolveLaunchEnv(context.Background())
	if err != nil || wired == nil {
		t.Fatalf("ResolveLaunchEnv after composeDaemon = %v (wired %v)", err, wired)
	}
	if env.Store == nil || env.Store != wired.Store || env.Bus == nil || env.Bus != wired.Bus || env.Effects == nil {
		t.Fatalf("launch env store %p bus %p, want the daemon's store %p and bus %p", env.Store, env.Bus, wired.Store, wired.Bus)
	}
}

// reapManifest is a process plugin for the shutdown test.
const reapManifest = `
id = "reapdemo"
name = "Reap Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
requires = ["host.secret_ref"]
`

// reapShim execs the argv listed in <self>.args (the flag-driven
// lifecycleplugin, run as an executable started with no arguments).
const reapShim = `package main

import (
	"os"
	"strings"
	"syscall"
)

func main() {
	raw, _ := os.ReadFile(os.Args[0] + ".args")
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	_ = syscall.Exec(argv[0], argv, os.Environ())
	os.Exit(10)
}
`

// installReapDemo installs reapdemo: the shim over lifecycleplugin -dir out
// -grandchild.
func installReapDemo(t *testing.T, exeDir, out string) {
	t.Helper()
	lp := goBuild(t, "../../internal/plugins/process/testdata/lifecycleplugin", t.TempDir(), "lifecycleplugin")
	src := t.TempDir()
	for name, body := range map[string]string{"main.go": reapShim, "go.mod": "module shim\n\ngo 1.26\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(exeDir, "reapdemo"), ".")
	cmd.Dir, cmd.Env = src, append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the shim: %v\n%s", err, b)
	}
	args := strings.Join([]string{lp, "-dir", out, "-grandchild"}, "\n")
	if err := os.WriteFile(filepath.Join(exeDir, "reapdemo.args"), []byte(args), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonShutdownReapsPluginChildren(t *testing.T) {
	exeDir, out := t.TempDir(), t.TempDir()
	installReapDemo(t, exeDir, out)
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := composeElevatedCtx(runCtx, t)
	attestedAdd(t, c.Call, "reapdemo", reapManifest)
	if _, err := plugins.LaunchInstalled(context.Background(), "reapdemo"); err != nil {
		t.Fatalf("LaunchInstalled under the composed daemon: %v", err)
	}
	child, grandchild := waitPid(t, out, "env-*"), waitPid(t, out, "grandchild.pid")
	cancel() // the daemon's run context ends
	for _, pid := range []int{child, grandchild} {
		waitGone(t, pid)
	}
}

// composeElevatedCtx is composeElevated with a run context the test owns.
func composeElevatedCtx(ctx context.Context, t *testing.T) composedElevated {
	t.Helper()
	ks := newSigningKeystore(t)
	pub := useElevationCustody(t, ks)
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	enrollKey(t, deps.Paths.DataDir(), pub)
	var got composedForTest
	opts, cleanups, err := composeDaemon(ctx, deps, func(r *rpc.Registry, m *rpc.SSEMux) { got.Registry, got.Events = r, m })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	got.Opts = opts
	return composedElevated{composedForTest: got, Deps: deps, KS: ks, Call: elevatedCaller(got.Registry, ks, runtime.NewSystemClock())}
}

// waitPid waits (bounded) for a file matching pattern in dir and returns
// the pid its name (env-<pid>) or content (grandchild.pid) carries.
func waitPid(t *testing.T, dir, pattern string) int {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		files, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(files) == 0 {
			continue
		}
		text := strings.TrimPrefix(filepath.Base(files[0]), "env-")
		if pattern == "grandchild.pid" {
			raw, _ := os.ReadFile(files[0])
			text = string(raw)
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
			return pid
		}
	}
	t.Fatalf("no %s in %s after 30s", pattern, dir)
	return 0
}

// waitGone waits (bounded) until pid answers ESRCH.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
	}
	t.Fatalf("pid %d still exists 30s after the daemon's run context ended", pid)
}
