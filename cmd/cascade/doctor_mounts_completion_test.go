// Purpose: proves doctor_mounts_completion.go's mount is real -- the
// production CompletionGateDoctorCheck reports FAIL when the CC harness's
// settings file does not yet name the completion-gate RPC method and OK
// once it does, and the check is actually present on the real
// productionCheckRegistry (the mutation this ticket's checks require:
// deleting the Register call turns TestProductionRegistryMountsCompletion
// GateCheck red).
// SPORT: cmd/cascade/doctor (CompletionGateDoctorCheck mount tests), CR-B D4.
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// completionGateFakeHome points HOME (plugins/claude's own hostEnvFixture
// pattern) at a temp dir, so the real HostPaths resolver never touches the
// operator's own settings file, and returns the settings.json path it
// resolves to.
func completionGateFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return filepath.Join(home, ".claude", "settings.json")
}

// TestProductionCompletionGateCheck_OKWhenNoHarnessInstalled pins the
// TestDoctorIsMountedOnRoot regression this mount must never repeat: a
// bare $HOME with no CC harness installed at all (no settings.json) is
// "nothing to wire into yet" -- OK, never a FAIL that would break every
// fresh install's `cascade doctor` (mirrors internal/context's own
// harnessResult).
func TestProductionCompletionGateCheck_OKWhenNoHarnessInstalled(t *testing.T) {
	completionGateFakeHome(t)
	res, err := productionCompletionGateCheck(doctorTestPaths(t), unreachableStatus).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with no settings file = %+v, want StatusOK (no harness installed)", res)
	}
}

// TestProductionCompletionGateCheck_FailsWhenAbsentFromSettings covers
// the real FAIL case: the settings file EXISTS (the harness IS installed)
// but does not name the completion-gate hook's RPC method.
func TestProductionCompletionGateCheck_FailsWhenAbsentFromSettings(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the harness integration refuses on Windows tier-2 (plugins/claude.hostPathsFor), so no settings file is ever read there")
	}
	settingsPath := completionGateFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := productionCompletionGateCheck(doctorTestPaths(t), unreachableStatus).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run with an installed settings file missing the hook = %+v, want StatusError", res)
	}
}

func TestProductionCompletionGateCheck_OKWhenPresentInSettings(t *testing.T) {
	settingsPath := completionGateFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"curl --unix-socket s -d {\"method\":\"fleet.sessions.completion_check\"} http://x"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := productionCompletionGateCheck(doctorTestPaths(t), unreachableStatus).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with settings file naming the method = %+v, want StatusOK", res)
	}
}

// TestProductionRegistryMountsCompletionGateCheck drives the REAL
// productionCheckRegistry and asserts the completion-gate check is
// present on it.
func TestProductionRegistryMountsCompletionGateCheck(t *testing.T) {
	useTempCustody(t)
	completionGateFakeHome(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, check := range reg.List() {
		if check.Name() == "completion-gate-hooks" {
			return
		}
	}
	t.Fatal("completion-gate-hooks is not registered on the real productionCheckRegistry")
}

// seedJobsDB writes a cascade.db with the jobs schema and one job in state
// under a fresh data directory, closes it, and returns the directory.
func seedJobsDB(t *testing.T, state jobs.JobState) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, doctorTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	err = jobs.NewStore(db).PutJob(context.Background(), jobs.Job{ID: "job-1", State: state, CreatedAt: 1, UpdatedAt: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal})
	if err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	return dir
}

// gateOver runs the mounted completion-gate check over dataDir and status
// with no harness installed (so the registration leg is OK).
func gateOver(t *testing.T, dataDir string, status daemonStatusSource) doctor.CheckResult {
	t.Helper()
	completionGateFakeHome(t)
	res, err := productionCompletionGateCheck(fixedDataDirPaths{dataDir: dataDir}, status).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestCompletionGateProbeDaemonDownActiveJobWarns: no daemon answers and a
// job is in each active state: warn. A terminal job and a machine with no
// database at all are the controls that must stay OK.
func TestCompletionGateProbeDaemonDownActiveJobWarns(t *testing.T) {
	for _, state := range activeJobStates {
		if res := gateOver(t, seedJobsDB(t, state), unreachableStatus); res.Status != doctor.StatusWarn {
			t.Errorf("daemon down with a %s job = %+v, want StatusWarn", state, res)
		}
	}
	if res := gateOver(t, seedJobsDB(t, jobs.JobStateAccepted), unreachableStatus); res.Status != doctor.StatusOK {
		t.Errorf("daemon down with only a terminal job = %+v, want StatusOK", res)
	}
	if res := gateOver(t, t.TempDir(), unreachableStatus); res.Status != doctor.StatusOK {
		t.Errorf("daemon down with no database = %+v, want StatusOK", res)
	}
}

// TestCompletionGateProbeDaemonUpIsOK: a daemon that answers status.get is
// reachable, whatever the jobs database holds, and the probe never opens it.
func TestCompletionGateProbeDaemonUpIsOK(t *testing.T) {
	dir := seedJobsDB(t, jobs.JobStateRunning)
	if res := gateOver(t, dir, statusOf(daemon.StatusResponse{})); res.Status != doctor.StatusOK {
		t.Fatalf("daemon up with a running job = %+v, want StatusOK", res)
	}
	reachable, active, err := completionGateLivenessProbe(fixedDataDirPaths{dataDir: dir}, statusOf(daemon.StatusResponse{}))(context.Background())
	if !reachable || active || err != nil {
		t.Fatalf("probe with the daemon up = (%v, %v, %v), want (true, false, nil)", reachable, active, err)
	}
}

// TestCompletionGateProbeUnreadableJobsWarns: a database whose jobs cannot be
// read is an unknown, which warns. The probe opens read-only: it neither
// applies the jobs schema nor leaves anything behind.
func TestCompletionGateProbeUnreadableJobsWarns(t *testing.T) {
	if dsn := jobsReadOnlyDSN("/x/cascade.db"); !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("probe DSN = %q, want mode=ro", dsn)
	}
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (x INTEGER)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	if res := gateOver(t, dir, unreachableStatus); res.Status != doctor.StatusWarn {
		t.Fatalf("daemon down over a database with no jobs table = %+v, want StatusWarn", res)
	}
	check, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = check.Close() }()
	var n int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'job'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the probe applied a jobs schema (job tables = %d, err = %v); it must open read-only", n, err)
	}
}
