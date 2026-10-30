//go:build !windows

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

type ciTestPusher struct {
	mu    sync.Mutex
	items []supervision.AttentionItem
}

func (p *ciTestPusher) Push(_ context.Context, it supervision.AttentionItem) (supervision.AttentionItem, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, it)
	return it, nil
}

type ciRecExec struct {
	mu   sync.Mutex
	subs []ci.SubJob
}

func (e *ciRecExec) Run(_ context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subs = append(e.subs, sj)
	return ci.SubJobResult{RunID: int64(len(e.subs)), RepoID: 1, Passed: true, ExecutorKind: "rec"}, nil
}

func ciGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=ci@example.invalid", "-c", "user.name=ci", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newCITestRuntime composes a fleet runtime over configTOML, loads that
// config and builds the CI runtime over it.
func newCITestRuntime(t *testing.T, configTOML string) (*fleetRig, *FleetRuntime, *CIRuntime, *ciTestPusher) {
	t.Helper()
	g := newFleetRig(t, configTOML)
	t.Setenv("HOME", filepath.Dir(g.deps.Paths.ConfigPath()))
	fr := g.start(t)
	cfg, err := runtime.Load(g.ctx, runtime.LoadOptions{Path: g.deps.Paths.ConfigPath(),
		Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	pusher := &ciTestPusher{}
	r, err := NewCISubsystem(g.ctx, fr, cfg, g.deps.Bus, pusher, g.clock)
	if err != nil {
		t.Fatalf("NewCISubsystem: %v", err)
	}
	return g, fr, r, pusher
}

// TestCISubsystemSingleDispatcher: one Dispatcher per daemon, and the only
// production ci.NewDispatcher( call in the module is subsystems_ci.go's.
func TestCISubsystemSingleDispatcher(t *testing.T) {
	_, fr, r, _ := newCITestRuntime(t, "")
	d := r.Dispatcher()
	if d == nil || r.Dispatcher() != d {
		t.Fatalf("Dispatcher() = %p then %p, want one non-nil dispatcher", d, r.Dispatcher())
	}
	if r.CIDB() != fr.JobsDB() {
		t.Fatal("CIDB is not FleetRuntime's one cascade.db handle")
	}
	if err := r.SetExecutor(&ciRecExec{}); err == nil {
		t.Fatal("SetExecutor after Dispatcher() succeeded, want a refusal")
	}
	var sites []string
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && p != root && (strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" || e.Name() == "vendor") {
			return filepath.SkipDir
		}
		if e.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(raw), "ci.NewDispatcher(") {
			sites = append(sites, filepath.ToSlash(p))
		}
		return rerr
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(sites) != 1 || sites[0] != "../../internal/daemon/subsystems_ci.go" {
		t.Fatalf("production ci.NewDispatcher( sites = %v, want exactly internal/daemon/subsystems_ci.go", sites)
	}
}

// TestCISubsystemConfigFromRuntime: [ci].affected_cmd in the loaded config
// reaches the Dispatcher's affected-target computation, observed through a
// real Checkpoint over a leased worktree with a recording command.
func TestCISubsystemConfigFromRuntime(t *testing.T) {
	recDir := t.TempDir()
	rec := filepath.Join(recDir, "changed.txt")
	toml := "[ci]\naffected_cmd = \"cat > " + rec + "; echo recorded-target\"\n"
	g, fr, r, _ := newCITestRuntime(t, toml)
	exe := &ciRecExec{}
	if err := r.SetExecutor(exe); err != nil {
		t.Fatalf("SetExecutor before start: %v", err)
	}
	ref := ciLeasedCommit(t, fr)
	if _, err := r.Dispatcher().Checkpoint(g.ctx, ref, r.Model(ref.RepoRoot)); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	raw, err := os.ReadFile(rec)
	if err != nil || strings.TrimSpace(string(raw)) != "src/a.txt" {
		t.Fatalf("affected_cmd stdin = %q (%v), want the changed path src/a.txt", raw, err)
	}
	exe.mu.Lock()
	defer exe.mu.Unlock()
	if len(exe.subs) == 0 {
		t.Fatal("no sub-job reached the executor")
	}
	for _, sj := range exe.subs {
		if len(sj.Plan.Targets) != 1 || sj.Plan.Targets[0] != "recorded-target" {
			t.Fatalf("sub-job %s targets = %v, want [recorded-target] from affected_cmd", sj.Kind, sj.Plan.Targets)
		}
	}
}

// ciLeasedCommit seeds a job, leases src/** of a fresh repo through the
// fleet runtime, commits src/a.txt in the leased worktree and returns the
// checkpoint's JobRef.
func ciLeasedCommit(t *testing.T, fr *FleetRuntime) ci.JobRef {
	t.Helper()
	ctx := context.Background()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	ciGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, repo, "add", ".")
	ciGit(t, repo, "commit", "-q", "-m", "init")
	base := ciGit(t, repo, "rev-parse", "HEAD")
	tier := provider.SensitivityInternal
	if err := fr.Jobs().PutJob(ctx, jobs.Job{ID: "job-ci", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "src/**", RiskClass: "low", MinTaskClass: "code",
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &tier}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	res, err := fr.Leases().Acquire(ctx, repo, "src/**", "job-ci")
	if err != nil || !res.Granted {
		t.Fatalf("Acquire = %+v, %v", res, err)
	}
	wt, err := fr.Worktrees().Create(ctx, res.Lease, repo)
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wt.Path, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "src", "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, wt.Path, "add", ".")
	ciGit(t, wt.Path, "commit", "-q", "-m", "work")
	return ci.JobRef{JobID: "job-ci", RepoRoot: repo, WorktreeRoot: wt.Path, Lease: res.Lease,
		LeaseEpoch: res.Lease.Epoch, CheckpointCommit: ciGit(t, wt.Path, "rev-parse", "HEAD"), BaseCommit: base,
		ScopePrefixes: []string{"src/"}, PlannedRisk: jobs.RiskClassLow, Sensitivity: tier}
}

// TestCIDispatcherComposed: the production composition registers exactly
// the local runner, and a real checkpoint through the composed Dispatcher
// resolves every kind to local: lint (configured) runs and passes, a kind
// with no [ci.local] command ends as the explicit unresolved non-pass, and
// the unregistered node and hosted steps are journaled as skipped (P1-CI-03).
func TestCIDispatcherComposed(t *testing.T) {
	if got := ciRunners(nil); len(got) != 1 || got[ci.ExecutorLocal] == nil {
		t.Fatalf("production runners = %v, want exactly {local}", got)
	}
	var journal bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&journal, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	g, fr, r, _ := newCITestRuntime(t, "[ci.local]\nlint = [\"true\"]\n")
	var mu sync.Mutex
	got := map[ci.RequirementKind]string{}
	d := r.Dispatcher()
	d.OnTerminal(func(ev ci.CIResultEvent) {
		mu.Lock()
		defer mu.Unlock()
		got[ev.Kind] = fmt.Sprintf("%s/%v", ev.ExecutorKind, ev.Passed)
	})
	ref := ciLeasedCommit(t, fr)
	if _, err := d.Checkpoint(g.ctx, ref, r.Model(ref.RepoRoot)); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got[ci.RequirementLint] != "local/true" || len(got) < 2 {
		t.Fatalf("terminals = %v, want lint local/true among several kinds", got)
	}
	for kind, v := range got {
		if kind != ci.RequirementLint && kind != ci.RequirementFormat && v != "unresolved/false" {
			t.Fatalf("terminal %s = %s, want unresolved/false (no [ci.local] command)", kind, v)
		}
	}
	log := journal.String()
	if !strings.Contains(log, "step=node") || !strings.Contains(log, "no node runner is registered") ||
		!strings.Contains(log, "step=github-actions") {
		t.Fatalf("journal lacks the skipped node and hosted steps:\n%s", log)
	}
}

// TestCIStoredSourceTrustFailsClosed: the daemon's SourceTrust rule trusts
// only a job present in this controller's store; a missing job and a store
// read error are never trusted (P1-CI-03, R-CI03-3).
func TestCIStoredSourceTrustFailsClosed(t *testing.T) {
	_, fr, _, _ := newCITestRuntime(t, "")
	ref := ciLeasedCommit(t, fr)
	trust := storedSourceTrust(fr.Jobs())
	if p, err := trust(context.Background(), ci.SubJob{Ref: ref}); err != nil || p != provider.ProvenanceTrusted {
		t.Fatalf("stored job: %q, %v; want trusted (positive control)", p, err)
	}
	missing := ref
	missing.JobID = "job-not-in-store"
	if p, err := trust(context.Background(), ci.SubJob{Ref: missing}); err == nil || p == provider.ProvenanceTrusted {
		t.Fatalf("missing job: %q, %v; want an error and never trusted", p, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := trust(ctx, ci.SubJob{Ref: ref}); err == nil || p == provider.ProvenanceTrusted {
		t.Fatalf("store read error: %q, %v; want an error and never trusted", p, err)
	}
}
