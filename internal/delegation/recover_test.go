// Purpose: crash-injected recovery of effect intents and orphaned children.
// All writes stay in temporary directories; synchronization uses process exit.

package delegation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

func TestDelegateRecoversPendingSpawnEffects(t *testing.T) {
	recoveryCrash(t, false)
}

func TestDelegateRecoverySweepsOrphanedChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if runtime.GOOS == "windows" {
		t.Skip("lease-marker process sweep is unsupported on Windows")
	}
	if os.Getenv("CASCADE_EFFECT_CHILD") == "" {
		err := agents.SweepLease(context.Background(), "clm_0123456789abcdef0123456789abcdef")
		if runtime.GOOS == "darwin" && err != nil && strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("sandbox denies the macOS process table required by the real sweep")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	recoveryCrash(t, true)
}

func recoveryEffects(t *testing.T, path string) *audit.EffectLog {
	t.Helper()
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	effects, err := audit.NewEffectLog(audit.New(store, fakeClock{now: t0}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return effects
}

// recoveryCrashChild follows the effect recorder's re-exec recipe. The fake
// records exactly one spawn, optionally leaving a real reapchild behind.
func recoveryCrashChild(t *testing.T, path string) {
	effects := recoveryEffects(t, path)
	ctx := context.Background()
	_, err := effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect,
		Actor: "agent-driver:recovery-test", Action: "agent.spawn", Key: "agent.spawn:restart",
		ParamsHash: audit.HashParams([]byte("restart"))})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	f, err := os.OpenFile(filepath.Join(dir, "spawns"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("spawn\n")
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if bin := os.Getenv("CASCADE_RECOVERY_REAPCHILD"); bin != "" {
		cmd := exec.Command(bin, "sleep", filepath.Join(dir, "ready.pid"))
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(3) // no ConfirmEffect, collector or teardown
}

func recoveryCrash(t *testing.T, orphan bool) {
	t.Helper()
	if path := os.Getenv("CASCADE_EFFECT_CHILD"); path != "" {
		recoveryCrashChild(t, path)
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := newChain(t)
	_, rec := c.add(c.root)
	marker := "clm_0123456789abcdef0123456789abcdef"
	if err := c.st.BindDelegationMarker(ctx, rec.ID, marker); err != nil {
		t.Fatal(err)
	}
	if orphan {
		if err := c.st.BindDelegationSpawn(ctx, rec.ID, "lease-restart", rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	w, err := AllocWorktree(home, rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "audit.db")
	runRecoveryCrash(ctx, t, home, path, marker, orphan)
	// Reopen the jobs handle as well as the audit store on restart.
	var seq int
	var name, dbPath string
	if err := c.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	if err := c.db.Close(); err != nil {
		t.Fatal(err)
	}
	c.db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	c.st = jobs.NewStore(c.db)
	assertRecovery(ctx, t, c, rec.ID, path, w, orphan)
}

func runRecoveryCrash(ctx context.Context, t *testing.T, home, path, marker string, orphan bool) {
	t.Helper()
	bin := ""
	if orphan {
		bin = filepath.Join(home, "reapchild")
		build := exec.CommandContext(ctx, goBinary(t), "build", "-o", bin, "../providers/agents/testdata/reapchild")
		build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(home, "go-cache"), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build reapchild: %v\n%s", err, out)
		}
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CASCADE_EFFECT_CHILD="+path,
		"CASCADE_RECOVERY_REAPCHILD="+bin, "CASCADE_LEASE_ID="+marker)
	out, err := cmd.CombinedOutput()
	if orphan {
		b, readErr := os.ReadFile(filepath.Join(home, "child.pid"))
		if readErr != nil {
			t.Fatalf("child pid: %v; crash output: %s", readErr, out)
		}
		pid, parseErr := strconv.Atoi(string(b))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		t.Cleanup(func() { p, _ := os.FindProcess(pid); _ = p.Kill() })
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("crash child: %v, want exit 3; %s", err, out)
	}
}

func assertRecovery(ctx context.Context, t *testing.T, c *chain, id, path string, w *Worktree, committed bool) {
	t.Helper()
	effects := recoveryEffects(t, path)
	phase, found, err := effects.EffectState(ctx, "agent.spawn:restart")
	if err != nil || !found || phase != audit.EffectIntent {
		t.Fatalf("before restart: %s %v %v", phase, found, err)
	}
	f := &fleetDouble{}
	swept := false
	d := RecoveryDeps{Effects: effects, Store: c.st, Fleet: f,
		ResolveLease: func(context.Context, jobs.DelegationRecord) (capacity.Lease, bool, error) {
			return capacity.Lease{ID: "lease-restart", ReservationID: "reservation", ExecutionID: "execution"}, committed, nil
		}, ReleaseWorktree: func(context.Context, jobs.DelegationRecord) error {
			if len(f.calls) != 0 || (!committed && !swept) {
				t.Fatal("worktree release order violated")
			}
			return w.Release()
		}}
	if !committed {
		d.sweepLease = func(_ context.Context, marker string) error {
			if marker != "clm_0123456789abcdef0123456789abcdef" || swept {
				t.Fatal("wrong marker or repeated sweep")
			}
			swept = true
			return nil
		}
	}
	for range 2 { // a second start does no additional work
		if err := RecoverPendingSpawns(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	assertRecoveryState(ctx, t, c, id, path, effects, f, w, committed)
}

func assertRecoveryState(ctx context.Context, t *testing.T, c *chain, id, path string, effects *audit.EffectLog, f *fleetDouble, w *Worktree, committed bool) {
	t.Helper()
	phase, _, err := effects.EffectState(ctx, "agent.spawn:restart")
	rec, recErr := c.st.GetDelegation(ctx, id)
	want := "abort"
	if committed {
		want = "release"
	}
	if err != nil || phase != audit.EffectUnknown || recErr != nil || rec.State != jobs.DelegationFailed || rec.Reason != "daemon restart" || !reflect.DeepEqual(f.calls, []string{want}) {
		t.Fatalf("recovered: phase %s (%v), record %s/%s (%v), fleet %v", phase, err, rec.State, rec.Reason, recErr, f.calls)
	}
	if _, err := os.Stat(w.Root); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	spawns, err := os.ReadFile(filepath.Join(filepath.Dir(path), "spawns"))
	if err != nil || string(spawns) != "spawn\n" {
		t.Fatalf("spawn count changed: %q %v", spawns, err)
	}
	if committed {
		assertRecoveryChildGone(t, filepath.Dir(path))
	}
}

func assertRecoveryChildGone(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	p, _ := os.FindProcess(pid)
	if p.Signal(syscall.Signal(0)) != nil {
		return
	}
	// Linux may retain an orphan as a zombie until PID 1 reaps it.
	stat, _ := os.ReadFile(filepath.Join("/proc", string(b), "stat"))
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
		return
	}
	t.Fatal("orphan survived startup recovery")
}

func TestRecoveryRetainsResourcesOnFailure(t *testing.T) {
	for _, stage := range []string{"sweep", "worktree", "resolver"} {
		t.Run(stage, func(t *testing.T) { recoveryFailure(t, stage) })
	}
}

func recoveryFailure(t *testing.T, stage string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	c := newChain(t)
	_, rec := c.add(c.root)
	if err := c.st.BindDelegationMarker(context.Background(), rec.ID, "clm_0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	f := &fleetDouble{}
	cause := cascade.New(cascade.KindUnavailable, "cleanup refused")
	d := RecoveryDeps{Effects: recoveryEffects(t, filepath.Join(home, "audit.db")), Store: c.st, Fleet: f,
		sweepLease: func(context.Context, string) error { return map[bool]error{true: cause}[stage == "sweep"] },
		ResolveLease: func(context.Context, jobs.DelegationRecord) (capacity.Lease, bool, error) {
			return capacity.Lease{ID: "held"}, false, map[bool]error{true: cause}[stage == "resolver"]
		}, ReleaseWorktree: func(context.Context, jobs.DelegationRecord) error {
			return map[bool]error{true: cause}[stage == "worktree"]
		}}
	if err := RecoverPendingSpawns(context.Background(), d); err != cause || err.Error() != cause.Error() {
		t.Fatalf("recovery error: %v", err)
	}
	after, err := c.st.GetDelegation(context.Background(), rec.ID)
	if err != nil || after.State != jobs.DelegationLive || len(f.calls) != 0 {
		t.Fatalf("failed cleanup retired resources: %+v %v %v", after, err, f.calls)
	}
}

func TestRecoveryRequiresDependencies(t *testing.T) {
	if err := RecoverPendingSpawns(context.Background(), RecoveryDeps{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("missing dependencies: %v", err)
	}
}
