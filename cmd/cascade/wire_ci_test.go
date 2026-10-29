//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

type ciFakeTicker struct{ c chan struct{} }

func (f *ciFakeTicker) C() <-chan struct{} { return f.c }
func (f *ciFakeTicker) Stop()              {}

type ciDaemonRig struct {
	deps daemonDeps
	tk   *ciFakeTicker
	repo string
}

func ciRigGit(t *testing.T, dir string, args ...string) string {
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

// newCIDaemonRig makes a temp HOME, a repo with one seed commit and daemon
// deps; the checkpoint poll runs on an injected ticker.
func newCIDaemonRig(t *testing.T) *ciDaemonRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r := &ciDaemonRig{deps: newRunTestDeps(t, nil), tk: &ciFakeTicker{c: make(chan struct{})}}
	saved := newCICheckpointTicker
	newCICheckpointTicker = func() runtime.Ticker { return r.tk }
	t.Cleanup(func() { newCICheckpointTicker = saved })
	r.repo, _ = filepath.EvalSymlinks(t.TempDir())
	ciRigGit(t, r.repo, "init", "-q", "-b", "main")
	writeCIFile(t, filepath.Join(r.repo, "README.md"), "seed\n")
	ciRigGit(t, r.repo, "add", ".")
	ciRigGit(t, r.repo, "commit", "-q", "-m", "init")
	return r
}

func writeCIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setCILocal writes a [ci.local] block running cmd for every list given.
func (r *ciDaemonRig) setCILocal(t *testing.T, cmd string, lists ...string) {
	t.Helper()
	cfg := "[ci.local]\n"
	for _, l := range lists {
		cfg += l + " = [\"" + cmd + "\"]\n"
	}
	writeCIFile(t, r.deps.Paths.ConfigPath(), cfg)
}

func (r *ciDaemonRig) tick(t *testing.T) {
	t.Helper()
	select {
	case r.tk.c <- struct{}{}:
	case <-time.After(testDaemonBound):
		t.Fatal("checkpoint watcher never took a tick")
	}
}

// leaseAndCommit seeds a low-planned internal job scoped to dir/**, leases
// dir/** through the daemon's LeaseManager, creates the worktree and
// commits one file under dir in it.
func (r *ciDaemonRig) leaseAndCommit(t *testing.T, fr *daemon.FleetRuntime, dir, file string) jobs.ResourceLease {
	t.Helper()
	ctx, tier := context.Background(), provider.SensitivityInternal
	if err := fr.Jobs().PutJob(ctx, jobs.Job{ID: "job-ci", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: dir + "/**", RiskClass: "low", MinTaskClass: "code",
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &tier}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	res, err := fr.Leases().Acquire(ctx, r.repo, dir+"/**", "job-ci")
	if err != nil || !res.Granted {
		t.Fatalf("Acquire = %+v, %v", res, err)
	}
	wt, err := fr.Worktrees().Create(ctx, res.Lease, r.repo)
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	writeCIFile(t, filepath.Join(wt.Path, dir, file), "a\n")
	ciRigGit(t, wt.Path, "add", ".")
	ciRigGit(t, wt.Path, "commit", "-q", "-m", "work")
	return res.Lease
}

// ciPayload decodes a ci_results payload with exact (64-bit) numbers.
func ciPayload(t *testing.T, ev events.Event) map[string]any {
	t.Helper()
	var p map[string]any
	dec := json.NewDecoder(bytes.NewReader(ev.Payload))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("decode %s: %v", ev.Kind, err)
	}
	return p
}

func jsonInt(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	i, err := n.Int64()
	if !ok || err != nil {
		t.Fatalf("%v is not a JSON integer", v)
	}
	return i
}

// outboxState returns the ci_dispatch outbox's total and unconfirmed rows.
func outboxState(t *testing.T, fr *daemon.FleetRuntime) (total, open int) {
	t.Helper()
	if err := fr.JobsDB().QueryRow(`SELECT COUNT(*) FROM jobs_outbox WHERE site = ?`,
		string(jobs.OutboxSiteCIDispatch)).Scan(&total); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	rows, err := jobs.UnconfirmedOutbox(context.Background(), fr.JobsDB(), jobs.OutboxSiteCIDispatch)
	if err != nil {
		t.Fatalf("UnconfirmedOutbox: %v", err)
	}
	return total, len(rows)
}

// awaitOutboxSettled polls the outbox (the consumer runs on its own
// goroutine) until rows exist and none is unconfirmed, or the bound passes.
func awaitOutboxSettled(t *testing.T, fr *daemon.FleetRuntime) (total, open int) {
	t.Helper()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	guard := time.After(testDaemonBound)
	for {
		if total, open = outboxState(t, fr); total > 0 && open == 0 {
			return total, open
		}
		select {
		case <-poll.C:
		case <-guard:
			return total, open
		}
	}
}

// TestDaemonCheckpointReachesDispatcher: in a composed daemon a commit in a
// leased job worktree produces via_stream=1 ci_runs and an all-confirmed
// ci_dispatch outbox. The dispatch passed the lease fence at the current
// epoch and carried the job row's tier. src/ reclassifies to normal, so the
// integration kind (no [ci.local] source) ends as an explicit not-configured,
// not-passed terminal instead of a pending row.
func TestDaemonCheckpointReachesDispatcher(t *testing.T) {
	r := newCIDaemonRig(t)
	r.setCILocal(t, "true", "lint", "build", "test")
	w, cancel, done := startTestDaemon(t, r.deps, observeWiring(t))
	defer func() { cancel(); awaitDaemonReturn(t, done) }()
	fr := w.Deps.Fleet
	if w.Deps.CI == nil || w.Deps.CI.CIDB() != fr.JobsDB() {
		t.Fatal("the ci registration did not set Deps.CI over the fleet runtime's handle")
	}
	lease := r.leaseAndCommit(t, fr, "src", "a.txt")
	r.tick(t)
	if total, open := awaitOutboxSettled(t, fr); total == 0 || open != 0 {
		t.Fatalf("ci_dispatch outbox = %d rows, %d unconfirmed; want every row confirmed", total, open)
	}
	all, err := w.Bus.Replay(context.Background(), ci.EventNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ev := range all {
		if ev.Kind != ci.EventKindCheckpointTerminal {
			continue
		}
		p := ciPayload(t, ev)
		if p["sensitivity"] != provider.SensitivityInternal.String() || p["job_id"] != "job-ci" {
			t.Fatalf("terminal = %v, want job-ci with the job row's internal tier", p)
		}
		via, err := ci.RunViaStream(context.Background(), fr.JobsDB(), jsonInt(t, p["run_id"]), jsonInt(t, p["repo_id"]))
		if err != nil || !via {
			t.Fatalf("RunViaStream(%v) = %v, %v; want via_stream=1", p, via, err)
		}
		got[fmt.Sprint(p["kind"])] = fmt.Sprintf("%v/%v", p["executor_kind"], p["passed"])
	}
	if got["lint"] != "local/true" || got["unit"] != "local/true" || got["integration"] != "local-not-configured/false" {
		t.Fatalf("terminals by kind = %v, want lint and unit passed locally, integration not-configured and not passed", got)
	}
	cur, ok, err := fr.Jobs().GetLease(context.Background(), lease.RepoID, lease.ScopeGlob)
	if err != nil || !ok || cur.Epoch != lease.Epoch {
		t.Fatalf("lease epoch moved: %+v %v %v", cur, ok, err)
	}
}

// TestDaemonStartResumesStreamOutbox: a dispatch left unfinished by a
// killed daemon is resumed by the next start's ci registration, before the
// socket serves, with exactly one ci_run per requirement kind.
func TestDaemonStartResumesStreamOutbox(t *testing.T) {
	r := newCIDaemonRig(t)
	r.setCILocal(t, "sleep 120", "lint")
	seen := observeWiring(t)
	w, cancel, done := startTestDaemon(t, r.deps, seen)
	sub, err := w.Bus.SubscribeFromHead(context.Background(), ci.EventNamespace, "test-ci-dispatched", 16)
	if err != nil {
		t.Fatal(err)
	}
	r.leaseAndCommit(t, w.Deps.Fleet, "docs", "a.md") // docs stay low: format and lint
	r.tick(t)
	for ev := range sub.Events {
		if ev.Kind == ci.EventKindCheckpointDispatched {
			break
		}
	}
	_ = sub.Unsubscribe()
	cancel() // the daemon dies with the first sub-job in flight
	awaitDaemonReturn(t, done)

	r.setCILocal(t, "true", "lint")
	openAtWire, detail := -1, ""
	addTestRegistration(t, daemonRegistration{Name: "test-ci-resume-probe", Phase: phaseMCPLast + 80, Order: 1,
		Wire: func(w *daemonWiring) error {
			_, openAtWire = outboxState(t, w.Deps.Fleet)
			for _, s := range w.Manifest.Snapshot() {
				if s.Name == ciSubsystem {
					detail = s.Detail
				}
			}
			return nil
		}})
	w2, cancel2, done2 := startTestDaemon(t, r.deps, seen)
	defer func() { cancel2(); awaitDaemonReturn(t, done2) }()
	var redispatched, confirmed int
	if _, err := fmt.Sscanf(detail, "resume: %d redispatched, %d confirmed", &redispatched, &confirmed); err != nil ||
		redispatched+confirmed == 0 || openAtWire != 0 {
		t.Fatalf("before serve: ci row %q, %d unconfirmed; want a resumed outbox with nothing left open", detail, openAtWire)
	}
	runs := streamRunCounts(t, w2.Deps.Fleet)
	if len(runs) != 2 || runs[firstKey(runs, "-format")] != 1 || runs[firstKey(runs, "-lint")] != 1 {
		t.Fatalf("stream ci_runs = %v, want exactly one format and one lint run", runs)
	}
}

// streamRunCounts returns the number of ci_run rows per stream run name.
func streamRunCounts(t *testing.T, fr *daemon.FleetRuntime) map[string]int {
	t.Helper()
	rows, err := fr.JobsDB().Query(`SELECT name, COUNT(*) FROM ci_run WHERE name LIKE 'stream-%' GROUP BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	runs := map[string]int{}
	for rows.Next() {
		name, n := "", 0
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		runs[name] = n
	}
	return runs
}

func firstKey(m map[string]int, suffix string) string {
	for k := range m {
		if strings.HasSuffix(k, suffix) {
			return k
		}
	}
	return ""
}
