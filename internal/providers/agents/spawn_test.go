// Purpose: the shim test fixture (a recording driver over a private registry,
// the real egress engine, a real effect log) and the tests for credential
// errors, exact environments and the profile lifecycle at the spawn.
// Constraints: every path is under t.TempDir(); HOME and USERPROFILE are a
// fresh temp dir; nothing touches the network or the operator's keychain.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-03.

package agents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var shimInstant = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// shimAudit is a real audit log and effect log over an in-memory store.
func shimAudit(t *testing.T) (*audit.Log, *audit.EffectLog) {
	t.Helper()
	log := audit.New(storetest.NewMemStore(), testkit.NewFrozenClock(shimInstant), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	return log, el
}

const (
	shimKeyEnv   = "TEST_CLAUDE_KEY"
	shimKeyValue = "synthetic-daemon-key-0001"
	shimToken    = "ptok-synthetic-0001"
)

// recDriver records every Spawn and stands in for a vendor driver.
type recDriver struct {
	provider.AgentProvider
	exe      string
	mu       sync.Mutex
	specs    []provider.AgentJobSpec
	spawnErr error
	before   func(provider.AgentJobSpec)
	state    provider.AgentRunState
	cancels  int
}

func (d *recDriver) Executable() string { return d.exe }

func (d *recDriver) Spawn(_ context.Context, s provider.AgentJobSpec) (provider.SpawnResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.before != nil {
		d.before(s)
	}
	if d.spawnErr != nil {
		return provider.SpawnResult{}, d.spawnErr
	}
	d.specs = append(d.specs, s)
	return provider.SpawnResult{JobID: s.JobID, ProcessGroupID: 4242}, nil
}

func (d *recDriver) Status(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
	return d.state, nil
}

func (d *recDriver) Cancel(context.Context, provider.AgentJobID) error {
	d.mu.Lock()
	d.cancels++
	d.mu.Unlock()
	return nil
}

func (d *recDriver) spawned() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.specs)
}

// emptyVault is a value source holding nothing, so only the detector pass runs.
type emptyVault struct{}

func (emptyVault) List(context.Context) ([]string, error) { return nil, nil }
func (emptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "empty")
}

// shimFix is one wired shim over a recording driver.
type shimFix struct {
	t        *testing.T
	shim     *Shim
	drv      *recDriver
	deps     ShimDeps
	root     string
	base     string
	exe      string
	env      map[string]string
	log      *audit.Log
	el       *audit.EffectLog
	swept    []string
	factorys int
	req      SpawnRequest
}

func newEngine(t *testing.T) *egress.Engine {
	t.Helper()
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	e, err := egress.NewEngine(egress.DefaultRegistry(), emptyVault{}, det)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func newShimFix(t *testing.T) *shimFix {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runtime profiles are not supported on windows")
	}
	isolateHome(t)
	f := &shimFix{t: t, root: t.TempDir(), base: filepath.Join(t.TempDir(), "profiles"), env: map[string]string{
		"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", shimKeyEnv: shimKeyValue}}
	f.exe = filepath.Join(t.TempDir(), "claude")
	writeFile(t, f.exe, "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(f.root, "src", "main.go"), "package main\n", 0o600)
	if err := os.Mkdir(filepath.Join(f.root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.drv = &recDriver{exe: f.exe, state: provider.AgentRunRunning}
	reg := &registry{rows: map[HarnessID]entry{}}
	reg.add(claudeRow, func(SpawnSite) (provider.AgentProvider, error) { f.factorys++; return f.drv, nil })
	res, err := newResolver(reg, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	f.log, f.el = shimAudit(t)
	f.deps = ShimDeps{
		Resolver: res, Auth: map[HarnessID]DriverAuth{HarnessClaude: {Harness: HarnessClaude, Class: CredentialAPIKey, KeyEnv: shimKeyEnv}},
		ProfileBase: f.base, Engine: func() (*egress.Engine, error) { return newEngine(t), nil }, Effects: f.el,
		ProxyJournal: ProxyJournal(f.log, f.el), Binaries: map[HarnessID]string{HarnessClaude: f.exe},
		Exe: filepath.Join(t.TempDir(), "cascade"), Getenv: func(k string) string { return f.env[k] },
		Clock: testkit.NewFrozenClock(shimInstant),
	}
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	f.req = SpawnRequest{Lane: "agent:claude", PrincipalToken: shimToken, LeaseMarker: marker, Job: provider.AgentJobSpec{
		Prompt: "work", Scope: provider.WorktreeScope{InScopePrefixes: []string{"src"}}, DataClass: provider.SensitivityInternal,
		ToolPolicy: provider.ToolPolicyNone, WorktreeRoot: f.root, JobID: "job-fix-1",
		Envelope: provider.SpawnEnvelope{Digest: strings.Repeat("a", 64), Principal: "p1", Roots: []string{filepath.ToSlash(f.root)},
			Sensitivity: provider.SensitivityInternal, ExpiresAt: shimInstant.Add(time.Hour)},
	}}
	f.build()
	return f
}

// build (re)creates the shim from f.deps and makes its sweep a recorder.
func (f *shimFix) build() {
	f.t.Helper()
	s, err := NewShim(f.deps)
	if err != nil {
		f.t.Fatalf("NewShim: %v", err)
	}
	s.sweep = func(_ context.Context, m string) error { f.swept = append(f.swept, m); return nil }
	f.shim = s
}

func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// profileDirs lists the job directories left under the profile base.
func (f *shimFix) profileDirs() []string {
	f.t.Helper()
	m, err := filepath.Glob(filepath.Join(f.base, "*", "*"))
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// refused asserts a refusal that started no child and left nothing behind.
func (f *shimFix) refused(err error) {
	f.t.Helper()
	if err == nil {
		f.t.Fatal("Spawn succeeded, want a refusal")
	}
	if n := f.drv.spawned(); n != 0 {
		f.t.Fatalf("%d children spawned on a refusal, want 0", n)
	}
	if d := f.profileDirs(); len(d) != 0 {
		f.t.Fatalf("profile directories left behind: %v", d)
	}
}

func TestShimRefusesOnCredentialError(t *testing.T) {
	cases := map[string]func(f *shimFix){
		"no credential choice": func(f *shimFix) { f.deps.Auth = map[HarnessID]DriverAuth{} },
		"class not offered": func(f *shimFix) {
			f.deps.Auth = map[HarnessID]DriverAuth{HarnessClaude: {Harness: HarnessClaude, Class: CredentialCompatKey, KeyEnv: shimKeyEnv}}
		},
		"key env unset":    func(f *shimFix) { delete(f.env, shimKeyEnv) },
		"newline in token": func(f *shimFix) { f.req.PrincipalToken = "tok\nen" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newShimFix(t)
			mutate(f)
			f.build()
			_, err := f.shim.Spawn(context.Background(), f.req)
			f.refused(err)
		})
	}
}

func TestShimChildEnvEqualsBuiltEnv(t *testing.T) {
	f := newShimFix(t)
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = h.Release(context.Background()) })
	spec := f.drv.specs[0]
	var proxy []string
	for _, kv := range spec.Env.Driver {
		if n, _, _ := strings.Cut(kv, "="); slices.Contains(proxyKeys, n) {
			proxy = append(proxy, kv)
		}
	}
	prof := RuntimeProfile{Home: spec.Env.Home, ConfigDir: filepath.Join(spec.Env.Home, "config"),
		Tmp: filepath.Join(filepath.Dir(spec.Env.Home), "tmp")}
	want, err := BuildDriverEnv(claudeRow, f.deps.Auth[HarnessClaude], prof, "job-fix-1", shimToken, f.req.LeaseMarker, proxy, f.deps.Getenv)
	if err != nil {
		t.Fatalf("BuildDriverEnv: %v", err)
	}
	if !slices.Equal(spec.Env.Driver, want.Driver) || !slices.Equal(spec.Env.Tool, want.Tool) {
		t.Fatalf("child env differs from BuildDriverEnv:\n got driver %q\nwant driver %q\n got tool %q\nwant tool %q",
			spec.Env.Driver, want.Driver, spec.Env.Tool, want.Tool)
	}
	for _, kv := range spec.Env.Tool {
		if strings.Contains(kv, shimKeyValue) {
			t.Fatalf("the tool environment carries the key: %q", kv)
		}
	}
}
