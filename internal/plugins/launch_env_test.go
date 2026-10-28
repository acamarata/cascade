//go:build !windows

// Purpose: the trusted-launch test fixture: a file-backed store, the effect
// log over it, an injected executable directory, the recorder lock (flock,
// as cmd/cascade installs it), a launch environment source, and trust
// granted only through the real witnessed plugin.add path. Real executables
// (cascade-github, a marker script) live under t.TempDir().
// SPORT: internal/plugins trusted launch tests (P1-PLG-03).
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

func init() { SetRecorderLock(testFlock) }

func testFlock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

var launchTestInstant = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// noopRegistrar accepts the plugin-process egress class registration.
type noopRegistrar struct{}

func (noopRegistrar) Register(process.EgressClass, process.EgressRegistrationConfig) error {
	return nil
}

// launchFix is one test's launch world.
type launchFix struct {
	dir, exeDir, lockDir, dbPath string
	store                        provider.Store
	log                          *audit.Log
	effects                      *audit.EffectLog
	env                          map[string]string
	trust                        trustFixture
}

// openLaunchStore opens the file-backed store at path.
func openLaunchStore(t *testing.T, path string) provider.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newLaunchFix builds the fixture and installs it as the launch source.
func newLaunchFix(t *testing.T) *launchFix {
	t.Helper()
	dir := t.TempDir()
	f := &launchFix{dir: dir, exeDir: filepath.Join(dir, "bin"), lockDir: filepath.Join(dir, "plugin-effects"),
		dbPath: filepath.Join(dir, "cascade.db"), env: map[string]string{"PATH": "/usr/bin:/bin", "HOME": dir}}
	if err := os.MkdirAll(f.exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetExecutableDirForTest(f.exeDir))
	f.store = openLaunchStore(t, f.dbPath)
	f.log = audit.New(f.store, testkit.NewFrozenClock(launchTestInstant), nil)
	el, err := audit.NewEffectLog(f.log)
	if err != nil {
		t.Fatal(err)
	}
	f.effects = el
	if _, err := RecoverPluginEffects(context.Background(), el, f.lockDir); err != nil {
		t.Fatalf("RecoverPluginEffects: %v", err)
	}
	f.trust = trustFixture{db: openDispatchTestDB(t), domains: storage.NewPluginDomainRegistry()}
	t.Cleanup(SetLaunchEnvSource(f.source))
	return f
}

// source is the fixture's LaunchEnvSource; getenv reads f.env only.
func (f *launchFix) source(context.Context) (LaunchEnv, error) {
	return LaunchEnv{Store: f.store, Effects: f.effects, Stderr: ioDiscard{}, Registrar: noopRegistrar{},
		Getenv: func(k string) string { return f.env[k] }, Clock: testkit.NewFrozenClock(launchTestInstant)}, nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

// grant trusts id through the real witnessed plugin.add path.
func (f *launchFix) grant(t *testing.T, id, manifest string) {
	t.Helper()
	if err := attested(t, "plugin.add", "plugin.add", pinnedArgs(t, id, manifest), f.addHandler()); err != nil {
		t.Fatalf("witnessed plugin.add of %s: %v", id, err)
	}
	rec, ok, err := LoadMetadata(context.Background(), f.store, id)
	if err != nil || !ok || rec.TrustTier != TrustTrusted {
		t.Fatalf("record after a witnessed add = %+v (ok=%v, err=%v), want trusted", rec, ok, err)
	}
}

// addHandler is the production plugin.add handler's decode and provision,
// over the fixture's store.
func (f *launchFix) addHandler() rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p boundInstallParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		m, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
		if err != nil {
			return nil, err
		}
		return ProvisionElevated(ctx, f.trust.db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "",
			f.store, f.trust.domains, m, p.Checksum, false, nil)
	}
}

// buildInto compiles pkg (a path relative to this package) to dir/name.
func buildInto(t *testing.T, pkg, dir, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	_ = os.Remove(out) // go build refuses to overwrite a non-object file
	cmd := exec.Command("go", "build", "-o", out, pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
	if err := os.Chmod(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

// installGitHub builds the real cascade-github into the executable dir.
func (f *launchFix) installGitHub(t *testing.T) string {
	t.Helper()
	return buildInto(t, "../../plugins/github", f.exeDir, cascadeGitHubPluginID)
}

// githubManifest is the real cascade-github manifest.
func githubManifest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../plugins/github/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// markerScript is a plugin executable that only leaves <path>.ran if run.
const markerScript = "#!/bin/sh\n: > \"$0.ran\"\nexit 0\n"

// installMarker writes the marker script as id's executable.
func (f *launchFix) installMarker(t *testing.T, id string) string {
	t.Helper()
	path := filepath.Join(f.exeDir, id)
	if err := os.WriteFile(path, []byte(markerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireNotStarted asserts a KindPolicyDenied refusal and that the marker
// executable at path never ran.
func requireNotStarted(t *testing.T, h *TrustedHandle, err error, path string, fragments ...string) {
	t.Helper()
	if h != nil || !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("LaunchInstalled = %v, %v; want a KindPolicyDenied refusal", h, err)
	}
	for _, frag := range fragments {
		if !strings.Contains(err.Error(), frag) {
			t.Fatalf("refusal %q does not name %q", err, frag)
		}
	}
	if _, serr := os.Stat(path + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("the refused executable ran (marker stat: %v)", serr)
	}
}

// effectPhases counts the stored effect records for key by phase.
func (f *launchFix) effectPhases(t *testing.T, key string) map[string]int {
	t.Helper()
	got := map[string]int{}
	for _, r := range f.allRecords(t) {
		if r.EffectKey == key {
			got[r.EffectPhase]++
		}
	}
	return got
}

// allRecords returns every audit record in the log.
func (f *launchFix) allRecords(t *testing.T) []audit.Record {
	t.Helper()
	var all []audit.Record
	filter := audit.Filter{}
	for {
		page, err := f.log.Query(context.Background(), filter)
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		all = append(all, page.Records...)
		if page.NextCursor == "" {
			return all
		}
		filter.Cursor = page.NextCursor
	}
}

// TestLaunchRefusesWithoutEffectWriter: a launch environment without an
// effect writer refuses before any record is read or anything starts.
func TestLaunchRefusesWithoutEffectWriter(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	f.grant(t, "trustdemo", trustManifest)
	restore := SetLaunchEnvSource(func(ctx context.Context) (LaunchEnv, error) {
		env, _ := f.source(ctx)
		env.Effects = nil
		return env, nil
	})
	defer restore()
	h, err := LaunchInstalled(context.Background(), "trustdemo")
	if h != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "effect writer") {
		t.Fatalf("LaunchInstalled without an effect writer = %v, %v; want KindUnavailable naming the effect writer", h, err)
	}
	if _, serr := os.Stat(exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the executable ran without an effect writer")
	}
	if n := len(f.allRecords(t)); n != 0 {
		t.Fatalf("%d audit records written by a refused launch, want 0", n)
	}
	SetLaunchEnvSource(nil)
	if _, err := LaunchInstalled(context.Background(), "trustdemo"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("LaunchInstalled with no source = %v, want KindUnavailable", err)
	}
}

var _ io.Writer = ioDiscard{}
