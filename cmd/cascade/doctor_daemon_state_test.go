// Purpose: proves the daemon-state doctor checks (doctor_daemon_state.go)
// against real subjects: status.get snapshots built from the daemon's own
// types, and a real bootstrapped cascade.db under t.TempDir() for storage.
// An unreachable daemon is a warning and never reaches the wrapped check; the
// census ignores disabled and skipped subsystems; storage runs only under
// --storage.
// SPORT: cmd/cascade/doctor (ADD, daemon-state check tests).
package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/providers/sqlite"
)

// unreachableStatus is a status source no daemon answers.
func unreachableStatus(context.Context) (daemon.StatusResponse, error) {
	return daemon.StatusResponse{}, errors.New("dial unix: no daemon")
}

// statusOf is a status source that answers resp.
func statusOf(resp daemon.StatusResponse) daemonStatusSource {
	return func(context.Context) (daemon.StatusResponse, error) { return resp, nil }
}

// subsystems builds a status.get Subsystems list from name -> state.
func subsystems(states map[string]daemon.SubsystemState) []daemon.SubsystemStatus {
	var out []daemon.SubsystemStatus
	for name, state := range states {
		out = append(out, daemon.SubsystemStatus{Name: name, State: state})
	}
	return out
}

// censusFor builds the mounted census check over status.
func censusFor(status daemonStatusSource) doctor.Check {
	return newCensusDoctorCheck(doctor.NewSubsystemCensusCheck(statusCensus{status: status}), status)
}

func runCheck(t *testing.T, c doctor.Check) doctor.CheckResult {
	t.Helper()
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("%s Run: %v", c.Name(), err)
	}
	return res
}

func TestDoctorCensusReportsDegradedSubsystem(t *testing.T) {
	res := runCheck(t, censusFor(statusOf(daemon.StatusResponse{Subsystems: subsystems(map[string]daemon.SubsystemState{
		"ipc-socket": daemon.SubsystemRunning, "scheduler": daemon.SubsystemError,
	})})))
	if res.Status != doctor.StatusError || !strings.Contains(res.Detail, "scheduler") || strings.Contains(res.Detail, "ipc-socket") {
		t.Fatalf("census with a failed subsystem = %+v, want StatusError naming only scheduler", res)
	}
}

// TestDoctorCensusIgnoresDisabledAndSkipped: a disabled and a skipped
// subsystem are not counted (a clean machine with an unconfigured bridge is
// healthy) but stay visible in Detail, and the real doctor command exits 0.
func TestDoctorCensusIgnoresDisabledAndSkipped(t *testing.T) {
	status := statusOf(daemon.StatusResponse{Subsystems: subsystems(map[string]daemon.SubsystemState{
		"ipc-socket": daemon.SubsystemRunning, "pa-bridge": daemon.SubsystemSkipped, "metrics": daemon.SubsystemDisabled,
	})})
	res := runCheck(t, censusFor(status))
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "1/1") {
		t.Fatalf("census = %+v, want StatusOK counting only the running subsystem (1/1)", res)
	}
	if !strings.Contains(res.Detail, "skipped: pa-bridge") || !strings.Contains(res.Detail, "disabled: metrics") {
		t.Fatalf("Detail = %q, want the skipped and disabled subsystems listed", res.Detail)
	}
	if _, err := execRootDoctor(t, testDoctorDeps(t, censusFor(status)), "doctor"); err != nil {
		t.Fatalf("cascade doctor on a clean machine with an unconfigured bridge: %v", err)
	}
}

func TestDoctorHookEventsWarnsOnDrops(t *testing.T) {
	check := newHookEventsDoctorCheck(statusOf(daemon.StatusResponse{Hooks: daemon.StatusHookFields{UnknownEventDrops: 3}}))
	if res := runCheck(t, check); res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "3 hook event") {
		t.Fatalf("hook-events with 3 drops = %+v, want StatusWarn naming the count", res)
	}
	clean := newHookEventsDoctorCheck(statusOf(daemon.StatusResponse{}))
	if res := runCheck(t, clean); res.Status != doctor.StatusOK {
		t.Fatalf("hook-events with no drops = %+v, want StatusOK", res)
	}
}

// countingCheck records whether the wrapped check ran.
type countingCheck struct {
	fakeCheck
	ran bool
}

func (c *countingCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	c.ran = true
	return c.fakeCheck.Run(ctx)
}

// TestDoctorDaemonUnreachableIsWarn: with no daemon the gated check warns
// "daemon not running" and never calls the wrapped check; the real census
// and hook-events checks behave the same.
func TestDoctorDaemonUnreachableIsWarn(t *testing.T) {
	inner := &countingCheck{fakeCheck: fakeCheck{name: "inner", status: doctor.StatusOK}}
	res := runCheck(t, daemonStateCheck{Check: inner, status: unreachableStatus})
	if res.Status != doctor.StatusWarn || res.Message != "daemon not running" || inner.ran {
		t.Fatalf("gated check with no daemon = %+v (inner ran: %v), want StatusWarn without calling the wrapped check", res, inner.ran)
	}
	for _, c := range []doctor.Check{censusFor(unreachableStatus), newHookEventsDoctorCheck(unreachableStatus)} {
		if got := runCheck(t, c); got.Status != doctor.StatusWarn {
			t.Errorf("%s with no daemon = %+v, want StatusWarn", c.Name(), got)
		}
	}
}

// bootstrappedDataDir returns a data directory holding a closed, bootstrapped
// cascade.db.
func bootstrappedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: doctorTestClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return dir
}

func storageCheckOver(dir string, status daemonStatusSource) doctor.Check {
	return newStorageDoctorCheck(fixedDataDirPaths{dataDir: dir}, status)
}

func TestDoctorStorageCheckSelectedByFlag(t *testing.T) {
	storageCheck := storageCheckOver(bootstrappedDataDir(t), unreachableStatus)
	other := &fakeCheck{name: "other-check", status: doctor.StatusOK, message: "OTHER-RAN"}
	out, err := execRootDoctor(t, testDoctorDeps(t, other, storageCheck), "doctor", "--storage")
	if err != nil {
		t.Fatalf("doctor --storage on a healthy database: %v\n%s", err, out)
	}
	if !strings.Contains(out, "storage probes passed") || strings.Contains(out, "OTHER-RAN") {
		t.Fatalf("doctor --storage output = %q, want only the storage check's result", out)
	}
}

func TestDoctorDefaultReportExcludesStorage(t *testing.T) {
	storageCheck := &fakeCheck{name: storageCheckName, status: doctor.StatusError, message: "STORAGE-RAN"}
	other := &fakeCheck{name: "other-check", status: doctor.StatusOK, message: "OTHER-RAN"}
	out, err := execRootDoctor(t, testDoctorDeps(t, other, storageCheck), "doctor")
	if err != nil {
		t.Fatalf("default doctor must not run the failing storage check: %v\n%s", err, out)
	}
	if strings.Contains(out, "STORAGE-RAN") || !strings.Contains(out, "OTHER-RAN") {
		t.Fatalf("default report = %q, want the other check and no storage check", out)
	}
}

// TestDoctorStorageFailingProbeIsError: an existing database that was never
// bootstrapped fails the schema probes, and that is StatusError.
func TestDoctorStorageFailingProbeIsError(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (x INTEGER)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	res := runCheck(t, storageCheckOver(dir, unreachableStatus))
	if res.Status != doctor.StatusError || !strings.Contains(res.Detail, "schema-version") {
		t.Fatalf("storage over an unbootstrapped database = %+v, want StatusError naming schema-version", res)
	}
}

// TestDoctorStorageLockHeldByDaemon: another holder of the exclusive lock is
// the daemon's own while status.get answers, and a conflict when nothing does.
func TestDoctorStorageLockHeldByDaemon(t *testing.T) {
	dir := bootstrappedDataDir(t)
	held, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("hold the store lock: %v", err)
	}
	defer func() { _ = held.Close() }()
	if res := runCheck(t, storageCheckOver(dir, statusOf(daemon.StatusResponse{}))); res.Status != doctor.StatusOK {
		t.Fatalf("storage with the daemon up and its lock held = %+v, want StatusOK", res)
	}
	if res := runCheck(t, storageCheckOver(dir, unreachableStatus)); res.Status != doctor.StatusError || !strings.Contains(res.Detail, "flock-probe") {
		t.Fatalf("storage with a lock held and no daemon = %+v, want StatusError naming flock-probe", res)
	}
}

func TestDoctorStorageMissingDatabaseWarns(t *testing.T) {
	if res := runCheck(t, storageCheckOver(t.TempDir(), unreachableStatus)); res.Status != doctor.StatusWarn {
		t.Fatalf("storage with no cascade.db = %+v, want StatusWarn (absence is not a pass)", res)
	}
}

// TestProductionRegistryNamesDaemonStateChecks drives the REAL registry.
func TestProductionRegistryNamesDaemonStateChecks(t *testing.T) {
	useTempCustody(t)
	completionGateFakeHome(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, name := range []string{"subsystem_census", "hook-events", storageCheckName, "config-permissions", "completion-gate-hooks"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("check %q is not registered on the real productionCheckRegistry", name)
		}
	}
}
