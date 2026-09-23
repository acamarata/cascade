//go:build integration

package jobs_test

// Purpose: P1-E29-W6-S60-T4 Path 2 -- kill -9 mid-DAG over a REAL daemon
//
//	child process (R-16.68c: never a context cancel, never an in-process
//	teardown), proving the production Resume wiring
//	(internal/daemon/subsystems_scheduler.go's RegisterScheduler, wired at
//	cmd/cascade/daemon_unix_scheduler_dag.go's wireJobScheduler --
//	DEFECT-scheduler-resume-never-called's fix) actually re-enters a
//	stale `running` job at `leased` on a REAL restart, with no evidence
//	duplication.
//
// CHOREOGRAPHY (R-14.313 consolidated rewrite; AMD-20260922/F3-12/F3-13,
// AMD-20260923/Z1-9 -- binding over this ticket's earlier text): (1) spawn
// the daemon once and SIGTERM it, so ITS OWN startup path creates and
// migrates {CASCADE_HOME}/data/cascade.db (never this suite's own schema
// helpers as the primary creation step -- see acceptance_resume_integration_rig_test.go's
// openPath2Rig doc for why calling them again afterward is still safe);
// (2) with NO daemon alive, seed job(running)+lease(held)+worktree+one
// lint EvidenceRecord+one KindCheckpoint journal entry through the real
// package APIs, where the SEEDING LeaseManager is built on a
// runtime.NewFixedClock already past DefaultLeaseDefaults' TTL+grace
// threshold, so the granted lease's IssuedAt is stale from the moment
// it is written -- no post-hoc mutation of a granted lease's IssuedAt;
// (3) spawn the REAL daemon, send a REAL syscall.SIGKILL, wait for exit;
// (4) restart the SAME binary over the SAME home so its startup Resume
// re-enters the stale job, and assert over the REAL unix socket
// (job.show) that it is `leased`, with exactly one evidence record;
// (5) SIGTERM that daemon (an orderly stop, not another kill); (6)
// reopen the store directly and drive leased->running->verifying->
// reviewing->accepted, asserting the ledger holds exactly 3 records.
//
// "leased -> running after restart" is explicitly NOT PROVEN by any W6
// production path (runSchedulerResume discards Resume's re-entry events
// and no persisted per-repo DAG coordinator exists yet); that transition
// is owned by P1-E41-W9-S79-T4 (register A1-288) and step (6) above
// drives it directly against the package API instead, the same "no
// coordinator yet" pattern acceptance_path1_test.go already establishes.
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
)

const path2JobID = "job-path2-kill9"
const path2Repo = "path2-repo"

// acceptanceCascadeBin is the real, source-built `cascade` binary
// TestMain constructs once for this test binary (Path 2 and Path 4
// share it -- neither builds its own copy, so both drive the SAME
// artifact, guide 31's "one source-built CLI" rule).
var acceptanceCascadeBin string

// TestMain builds acceptanceCascadeBin once before any test in this
// (integration-tagged) binary runs, then delegates to the ordinary test
// runner. A build failure aborts the whole binary with the compiler's
// own output, rather than letting Path 2/4 fail individually with a
// confusing "no such file" once acceptanceCascadeBin is empty.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cascade-acceptance-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acceptance TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = acceptanceRepoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance TestMain: go build ./cmd/cascade: %v\n%s\n", err, out)
		os.Exit(1)
	}
	acceptanceCascadeBin = bin
	os.Exit(m.Run())
}

// acceptanceRepoRoot walks up from this package's working directory to
// the module root, so the build above runs with `./cmd/cascade` resolved
// against the module regardless of where `go test` was invoked from.
func acceptanceRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// path2Home is one Path 2/4 scenario's isolated CASCADE_HOME (shared with
// acceptance_rpc_integration_test.go's Path 4, which needs the same
// init/spawn/kill lifecycle plus sockPath for its own real RPC dial).
type path2Home struct {
	env      []string
	home     string // the project working directory `cascade` runs from (exists from the start)
	dataDir  string // {CASCADE_HOME}/data -- created by the daemon's own startup, not by `init`
	dbPath   string
	sockPath string // CASCADE_SOCKET -- see newPath2Home for why this is NOT {CASCADE_HOME}/daemon.sock
	repoRoot string
}

// newPath2Home builds a fresh CASCADE_HOME via a real `cascade init`,
// matching internal/acceptance/j_s21_script_test.go's own established
// drillEnv pattern.
//
// The unix socket lives under a SHORT path this function mints
// separately from t.TempDir() (`os.MkdirTemp("/tmp", ...)`, never
// `t.TempDir()`'s own nested `/var/folders/.../T/<TestName>/NNN/` root):
// `sockaddr_un.sun_path` has a ~104-byte OS limit, and `t.TempDir()`'s
// path already embeds this test's own (long) name -- `bind` fails with
// EINVAL, not ENAMETOOLONG, so a length problem here reads exactly like
// a permissions problem until measured (verified against the real
// daemon: `bind: invalid argument`).
func newPath2Home(t *testing.T) path2Home {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cascadeHome := filepath.Join(home, ".cascade")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll home: %v", err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "cs")
	if err != nil {
		t.Fatalf("MkdirTemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")
	h := path2Home{
		env: []string{
			"HOME=" + home, "CASCADE_HOME=" + cascadeHome, "CASCADE_SOCKET=" + sockPath,
			"CASCADE_NO_INPUT=1", "PATH=" + os.Getenv("PATH"),
		},
		home:     home,
		dataDir:  filepath.Join(cascadeHome, "data"),
		dbPath:   filepath.Join(cascadeHome, "data", "cascade.db"),
		sockPath: sockPath,
		repoRoot: realGitRepo(t),
	}
	out, err := h.run(t, "init", "--yes", "--no-daemon")
	if err != nil {
		t.Fatalf("cascade init: %v\n%s", err, out)
	}
	return h
}

// run executes one `cascade` command against this home and returns its
// combined output.
func (h path2Home) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(acceptanceCascadeBin, args...) //nolint:gosec // built by this suite's own TestMain.
	cmd.Env = h.env
	cmd.Dir = h.home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// spawnDaemon starts `cascade daemon run` as a REAL child process and
// waits for it to answer `cascade status` (proving startup -- including
// the synchronous schema migration and RegisterScheduler/Resume pass,
// which run before the socket ever listens -- has completed), never
// dialing the socket directly.
func (h path2Home) spawnDaemon(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(acceptanceCascadeBin, "daemon", "run") //nolint:gosec // built by this suite's own TestMain.
	cmd.Env = h.env
	cmd.Dir = h.home
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}
	defer func() { _ = logFile.Close() }()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	for range 150 {
		if _, err := h.run(t, "status"); err == nil {
			return cmd
		}
		if cmd.ProcessState != nil {
			break // the daemon already exited; stop polling and report its log
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	out, _ := os.ReadFile(logPath)
	t.Fatalf("daemon never answered `cascade status`; daemon log:\n%s", out)
	return nil
}

// sigkillAndWait sends a REAL syscall.SIGKILL (R-16.68c: never a context
// cancel, never an in-process teardown) and waits for the process to
// fully exit, so its sqlite handle on cascade.db is genuinely released
// before this file's own next connection opens.
func sigkillAndWait(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL daemon: %v", err)
	}
	_ = cmd.Wait() // a killed process's Wait error (non-zero exit) is expected, not a test failure
}

// sigtermAndWait sends a REAL syscall.SIGTERM -- an ORDERLY stop, unlike
// sigkillAndWait -- and waits for full exit. Used for the priming spawn
// (step 1: create/migrate cascade.db) and the final stop before this
// file drives the resumed job's remaining transitions directly (never a
// kill where a graceful shutdown is what the choreography actually
// calls for).
func sigtermAndWait(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM daemon: %v", err)
	}
	_ = cmd.Wait()
}

// TestAcceptancePath2KillResume is the HOW 3 / acceptance-criteria kill
// -9 mid-DAG test. See this file's header comment for the full six-step
// choreography (prime -> seed stale -> kill -> restart+assert over RPC
// -> stop -> complete).
func TestAcceptancePath2KillResume(t *testing.T) {
	home := newPath2Home(t)

	// Step 1: prime -- the daemon's OWN startup creates/migrates
	// cascade.db; stopped with an ORDERLY SIGTERM, not a kill.
	primer := home.spawnDaemon(t)
	sigtermAndWait(t, primer)

	// Step 2: seed, with no daemon alive, a job(running)+lease(held,
	// ALREADY STALE via a fixed-clock LeaseManager)+worktree+one lint
	// evidence record+one KindCheckpoint journal entry.
	seedPath2Stale(t, home.dbPath, home.repoRoot)

	// Step 3: the REAL daemon this test kills.
	daemon := home.spawnDaemon(t)
	sigkillAndWait(t, daemon)

	// A SEPARATE real kill-9 finding: clear the retention scheduler's
	// stale advisory lock the SIGKILL left behind (see
	// clearSchedulerAdvisoryLock's own doc comment) -- otherwise the
	// restart below never starts at all.
	clearSchedulerAdvisoryLock(t, home.dbPath)

	// Step 4: restart the SAME binary/home; its startup Resume re-enters
	// the stale job. Assert `leased` over the REAL socket (job.show),
	// never by reopening the db while this daemon still holds it.
	restarted := home.spawnDaemon(t)
	var shown struct {
		ID    string `json:"ID"`
		State string `json:"State"`
	}
	dialRPC(t, home.sockPath, "job.show", map[string]any{"id": path2JobID}, &shown)
	if shown.State != string(jobs.JobStateLeased) {
		t.Fatalf("job.show state after restart = %q, want %q (the restarted binary's Resume must have re-entered it)", shown.State, jobs.JobStateLeased)
	}
	assertPath2OneEvidenceRecord(t, home.dbPath)

	// Step 5: an orderly stop -- no concurrent writer on cascade.db while
	// this file drives the remaining transitions directly (no production
	// coordinator exists yet either, per subsystems_scheduler.go's
	// disclosed CONTRACT DEVIATION).
	sigtermAndWait(t, restarted)

	// Step 6: complete leased -> running -> ... -> accepted directly.
	driveP2ToAccepted(t, home.dbPath)
}
