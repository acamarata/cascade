//go:build !windows

package claude

// Purpose: the job child's lifetime: stdin is the null device, the Spawn
//   context is not the child's kill switch, and the R-21.174 ladder over the
//   whole process group (the confirm probes 03/03b/03c/12/13/14/15 as tests).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestHeadlessSpawnStdinNull(t *testing.T) {
	f := newFx(t)
	out := filepath.Join(f.dir, "stdin")
	f.body = "if [ -c /dev/stdin ]; then echo chr > " + out + "; fi\ncat >> " + out + "\n" + f.body
	if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "chr\n" {
		t.Fatalf("child stdin: %q, want the null character device with no input", b)
	}
}

// spawnSleeper spawns a job whose body is body (it should not exit by
// itself) and returns the driver and job.
func spawnSleeper(t *testing.T, f *fx, cfg Config, body string) (*Driver, provider.SpawnResult) {
	t.Helper()
	f.body = body
	cfg.Binary = f.script()
	d := f.driver(cfg)
	res, err := d.Spawn(context.Background(), f.spec())
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return d, res
}

func state(t *testing.T, d *Driver, id provider.AgentJobID) provider.AgentRunState {
	t.Helper()
	s, err := d.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaudeCancelProcessGroupConfirmed(t *testing.T) {
	t.Run("running leader", cancelRunningLeader)
	t.Run("member ignores TERM with a background ctx", cancelTermIgnoringMember)
	t.Run("leader exits 0 leaving a detached member", cancelAfterDetachedMember)
	t.Run("unconfirmed then a second Cancel completes", cancelUnconfirmedThenComplete)
	t.Run("spawn ctx is not the kill switch", cancelSpawnCtxNotKillSwitch)
}

func cancelRunningLeader(t *testing.T) {
	f := newFx(t)
	pidf := filepath.Join(f.dir, "pid")
	d, res := spawnSleeper(t, f, f.config(), "echo $$ > "+pidf+"\nexec sleep 30\n")
	pid := readPid(t, pidf)
	if res.ProcessGroupID != pid {
		t.Fatalf("ProcessGroupID %d, want the leader's own group %d", res.ProcessGroupID, pid)
	}
	if err := d.Cancel(context.Background(), res.JobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if alive(pid) || state(t, d, res.JobID) != provider.AgentRunCancelled {
		t.Fatalf("leader alive=%v state=%s", alive(pid), state(t, d, res.JobID))
	}
}

func cancelTermIgnoringMember(t *testing.T) {
	f := newFx(t)
	gp := filepath.Join(f.dir, "gpid")
	d, res := spawnSleeper(t, f, f.config(),
		"sh -c 'trap \"\" TERM; exec sleep 30' >/dev/null 2>&1 </dev/null &\necho $! > "+gp+"\nexec sleep 30\n")
	g := readPid(t, gp)
	start := time.Now()
	if err := d.Cancel(context.Background(), res.JobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if alive(g) || time.Since(start) > 5*time.Second || state(t, d, res.JobID) != provider.AgentRunCancelled {
		t.Fatalf("member alive=%v after %s state=%s", alive(g), time.Since(start), state(t, d, res.JobID))
	}
}

func cancelAfterDetachedMember(t *testing.T) {
	f := newFx(t)
	gp := filepath.Join(f.dir, "gpid")
	f.body = "sleep 30 >/dev/null 2>&1 </dev/null &\necho $! > " + gp + "\n" + f.body
	d := f.driver(f.config())
	res, cr, err := run(t, d, f.spec())
	g := readPid(t, gp)
	if alive(g) {
		t.Fatal("the detached member outlived its job")
	}
	if err == nil || state(t, d, res.JobID) != provider.AgentRunFailed || cr.Output != "" {
		t.Fatalf("a straggler with an unverifiable/vendor env must fail the run: err=%v state=%s", err, state(t, d, res.JobID))
	}
	if cerr := d.Cancel(context.Background(), res.JobID); cerr != nil {
		t.Fatalf("Cancel after a terminal state: %v", cerr)
	}
}

func cancelUnconfirmedThenComplete(t *testing.T) {
	f := newFx(t)
	cfg := f.config()
	cfg.Defaults.CancelGrace = 10 * time.Second
	gp := filepath.Join(f.dir, "gpid")
	d, res := spawnSleeper(t, f, cfg, "trap '' TERM\necho $$ > "+gp+"\nwhile :; do sleep 1; done\n")
	g := readPid(t, gp)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := d.Cancel(ctx, res.JobID); err != provider.ErrCancelUnconfirmed {
		t.Fatalf("Cancel(short ctx) = %v, want ErrCancelUnconfirmed", err)
	}
	if state(t, d, res.JobID) != provider.AgentRunCancelling {
		t.Fatalf("state %s, want cancelling", state(t, d, res.JobID))
	}
	_ = syscall.Kill(g, syscall.SIGKILL) // the group empties on its own now
	if err := d.Cancel(context.Background(), res.JobID); err != nil || state(t, d, res.JobID) != provider.AgentRunCancelled {
		t.Fatalf("second Cancel = %v, state %s", err, state(t, d, res.JobID))
	}
}

func cancelSpawnCtxNotKillSwitch(t *testing.T) {
	f := newFx(t)
	pidf := filepath.Join(f.dir, "pid")
	f.body = "echo $$ > " + pidf + "\nexec sleep 30\n"
	d := f.driver(f.config())
	ctx, cancel := context.WithCancel(context.Background())
	res, err := d.Spawn(ctx, f.spec())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	pid := readPid(t, pidf)
	if !alive(pid) || state(t, d, res.JobID) != provider.AgentRunRunning {
		t.Fatal("cancelling the Spawn context killed the job")
	}
	_ = d.Cancel(context.Background(), res.JobID)
}

func TestSpawnRefusesWhenProcessGroupLookupFails(t *testing.T) {
	f := newFx(t)
	f.body = "exec sleep 30\n"
	orig, child := getpgid, 0
	getpgid = func(pid int) (int, error) { child = pid; return 0, syscall.EINVAL }
	defer func() { getpgid = orig }()
	_, err := f.driver(f.config()).Spawn(context.Background(), f.spec())
	if err == nil || !strings.Contains(err.Error(), "process group") || child == 0 {
		t.Fatalf("Spawn = %v (lookup saw pid %d), want the process-group refusal", err, child)
	}
	if alive(child) {
		t.Fatal("the child survived a refused spawn")
	}
	getpgid = func(pid int) (int, error) { child = pid; return pid + 1, nil }
	if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); err == nil || alive(child) {
		t.Fatalf("Spawn accepted a child outside its own group (err %v, alive %v)", err, alive(child))
	}
}

func TestLifecycleUnknownJobAndMessage(t *testing.T) {
	f := newFx(t)
	d := f.driver(f.config())
	ctx := context.Background()
	if _, err := d.Status(ctx, "nope"); err != provider.ErrJobNotFound {
		t.Fatalf("Status: %v", err)
	}
	if err := d.Cancel(ctx, "nope"); err != provider.ErrJobNotFound {
		t.Fatalf("Cancel: %v", err)
	}
	if err := d.Message(ctx, "nope", "x"); err != provider.ErrJobNotFound {
		t.Fatalf("Message: %v", err)
	}
	if _, err := d.Collect(ctx, "nope"); err != provider.ErrJobNotFound {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := d.Artifacts(ctx, "nope"); err != provider.ErrJobNotFound {
		t.Fatalf("Artifacts: %v", err)
	}
	pidf := filepath.Join(f.dir, "pid")
	d, res := spawnSleeper(t, f, f.config(), "echo $$ > "+pidf+"\nexec sleep 30\n")
	if err := d.Message(ctx, res.JobID, "next"); err != provider.ErrMessageUnsupported {
		t.Fatalf("Message(live job) = %v, want ErrMessageUnsupported", err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := d.Collect(short, res.JobID); err == nil {
		t.Fatal("Collect returned before a running job finished")
	}
	_ = d.Cancel(ctx, res.JobID)
	if _, err := d.Collect(ctx, res.JobID); err == nil {
		t.Fatal("Collect of a cancelled job returned no error")
	}
	if err := signalGroup(0, procgroup.SigTerm); err == nil {
		t.Fatal("signalGroup(0) accepted an invalid group")
	}
}

// TestStrayPipeHolderDoesNotHangCollect: a process that leaves the group
// (setsid) but keeps stdout open cannot hold Collect past pumpDrainBound.
func TestStrayPipeHolderDoesNotHangCollect(t *testing.T) {
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("perl is needed to call setsid from the fake harness")
	}
	f := newFx(t)
	pidf := filepath.Join(f.dir, "escapee")
	f.body = perl + ` -e 'use POSIX; if (fork) { exit 0 } POSIX::setsid(); open(F, ">", "` + pidf +
		`"); print F "$$"; close F; sleep 30' &` + "\n" + f.body
	start := time.Now()
	_, _, err = run(t, f.driver(f.config()), f.spec())
	escapee := readPid(t, pidf)
	defer func() { _ = syscall.Kill(escapee, syscall.SIGKILL) }()
	if err != nil || time.Since(start) > 15*time.Second {
		t.Fatalf("Collect = %v after %s", err, time.Since(start))
	}
}

func TestStartFailuresUndoPerSpawnState(t *testing.T) {
	t.Run("binary vanishes after the probes", func(t *testing.T) {
		f := newFx(t)
		cfg := f.config()
		script, _ := os.ReadFile(cfg.Binary)
		patched := strings.Replace(string(script), `*" stream-json "*) `, `*" stream-json "*) rm -f "$0"; `, 1)
		mustWrite(t, cfg.Binary, patched)
		_ = os.Chmod(cfg.Binary, 0o700)
		if _, err := f.driver(cfg).Spawn(context.Background(), f.spec()); err == nil || exists(f.jobRan) {
			t.Fatalf("Spawn = %v", err)
		}
		if exists(filepath.Join(f.root, ".claude", provider.CallbackFileName)) {
			t.Fatal("a failed start left its token file behind")
		}
	})
	t.Run("crypto draw fails", spawnWithFailingCryptoDraws)
	t.Run(".claude is a file", func(t *testing.T) {
		f := newFx(t)
		mustWrite(t, filepath.Join(f.root, ".claude"), "not a dir")
		cfg := f.config()
		cfg.HookPackPath = ""
		if _, err := f.driver(cfg).Spawn(context.Background(), f.spec()); err == nil || exists(f.jobRan) {
			t.Fatalf("Spawn = %v", err)
		}
	})
	t.Run("stale token file is replaced, a directory refuses", func(t *testing.T) {
		f := newFx(t)
		mustMkdir(t, filepath.Join(f.root, ".claude"))
		mustWrite(t, filepath.Join(f.root, ".claude", provider.CallbackFileName), "stale")
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatalf("stale token file: %v", err)
		}
		mustMkdir(t, filepath.Join(f.root, ".claude", provider.CallbackFileName))
		if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); !errors.Is(err, ErrWorktreeEscape) {
			t.Fatalf("directory at the token path: %v", err)
		}
	})
}

func spawnWithFailingCryptoDraws(t *testing.T) {
	f := newFx(t)
	orig := cryptoRead
	defer func() { cryptoRead = orig }()
	for _, okDraws := range []int{0, 1} {
		n := okDraws
		cryptoRead = func(b []byte) (int, error) {
			if n > 0 {
				n--
				return orig(b)
			}
			return 0, syscall.EIO
		}
		if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); err == nil || exists(f.jobRan) {
			t.Fatalf("Spawn with %d good draws = %v", okDraws, err)
		}
	}
}
