package main

// Purpose (this file): mounts hookpacks.CompletionGateDoctorCheck into
//   `cascade doctor` (R-16.47; CR-B round-3 D4 -- a doctor check nothing
//   runs is dead code). `cascade doctor` builds a fresh process per
//   invocation and never calls wireCompletionHookPack (the DAEMON-only
//   composition root, cmd/cascade/hooks.go), so hookpacks.DefaultRegistry
//   is always empty here -- mounting the check against it would report a
//   permanent, meaningless FAIL (testonly-allow.json's own retired entry
//   names this exact gap). This file never touches DefaultRegistry: it
//   builds its OWN local *hookpacks.HookRegistry and populates it only
//   when the CC harness's real installed settings file already carries
//   hookpacks.MethodCompletionCheck -- an exported, pure string constant
//   naming the completion-gate hook's own RPC method, present in every
//   rendered Stop/TaskCompleted command (completion_hook_command.go) --
//   the one signal doctor CAN observe without a live daemon.
// Inputs: the CC harness's real settings file (plugins/claude.HostPaths,
//   the same production path resolution `cascade init`'s own install
//   uses).
// Outputs: CompletionGateDoctorCheck mounted, StatusError only when the
//   settings file EXISTS but does not name the method; StatusOK when it
//   does, AND when no settings file exists at all -- mirroring
//   internal/context's own harnessResult ("a machine with no harness
//   installed is a machine cascade has nothing to wire into yet ...
//   reporting that as a problem would make every server install fail its
//   own doctor"; TestDoctorIsMountedOnRoot pins the same "healthy install
//   on a bare $HOME never errors" contract this file must not break).
// Constraints: CompletionCheck/ResolveJob on the placeholder gate/
//   resolver below are never called by CompletionGateDoctorCheck.Run (it
//   only inspects the registry's own registered descriptors) -- this is
//   a structural registration guard, never a stand-in for a security
//   seam.
// SPORT: cmd/cascade/doctor (CompletionGateDoctorCheck mount), CR-B D4.

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
)

// productionCompletionGateCheck builds the mounted doctor.Check. Its
// LivenessProbe is completionGateLivenessProbe, so the check also warns when
// the daemon is down while a job is active.
func productionCompletionGateCheck(paths runtime.PathProvider, status daemonStatusSource) *hookpacks.CompletionGateDoctorCheck {
	return hookpacks.NewCompletionGateDoctorCheck(installedCompletionGateRegistry(), completionGateLivenessProbe(paths, status))
}

// activeJobStates are the job states in which a completion check can be
// pending: leased, running, verifying, reviewing and cancelling.
var activeJobStates = []jobs.JobState{
	jobs.JobStateLeased, jobs.JobStateRunning, jobs.JobStateVerifying, jobs.JobStateReviewing, jobs.JobStateCancelling,
}

// completionGateLivenessProbe answers hookpacks.LivenessProbe. A status.get
// answer means the daemon is reachable and no job question arises
// (true, false, nil). With no answer it reads the jobs database read-only:
// any active job, or a database it cannot read, is (false, true, nil), so the
// check warns; no database file at all is (false, false, nil), a machine
// where nothing has ever run.
func completionGateLivenessProbe(paths runtime.PathProvider, status daemonStatusSource) hookpacks.LivenessProbe {
	return func(ctx context.Context) (bool, bool, error) {
		if _, err := status(ctx); err == nil {
			return true, false, nil
		}
		return false, jobActiveInDatabase(ctx, paths.DataDir()), nil
	}
}

// jobsReadOnlyDSN is the DSN the probe opens cascade.db with: mode=ro, so
// the probe can neither write a row nor apply a schema.
func jobsReadOnlyDSN(dbPath string) string {
	return "file:" + dbPath + "?mode=ro&_busy_timeout=5000"
}

// jobActiveInDatabase reports whether cascade.db under dataDir holds a job in
// an active state. It is true when the database cannot be read (an unknown
// is not a pass) and false only when the file does not exist or no job is
// active.
func jobActiveInDatabase(ctx context.Context, dataDir string) bool {
	if dataDir == "" {
		return true
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return false
	} else if err != nil {
		return true
	}
	db, err := sql.Open("sqlite", jobsReadOnlyDSN(dbPath))
	if err != nil {
		return true
	}
	defer func() { _ = db.Close() }()
	store := jobs.NewStore(db)
	for _, state := range activeJobStates {
		rows, _, err := store.ListJobs(ctx, jobs.JobFilter{State: state, Limit: 1})
		if err != nil || len(rows) > 0 {
			return true
		}
	}
	return false
}

// installedCompletionGateRegistry builds a throwaway local HookRegistry
// (never hookpacks.DefaultRegistry) and registers the real
// completion-gate pack on it -- which makes CompletionGateDoctorCheck.Run
// report OK -- unless the harness's settings file EXISTS and is missing
// the RPC method: an installed-but-unwired harness is the one real
// FAIL case; no settings file at all is "nothing installed yet", OK.
func installedCompletionGateRegistry() *hookpacks.HookRegistry {
	reg := hookpacks.NewHookRegistry()
	if completionGateMissingFromAnInstalledSettingsFile() {
		return reg
	}
	_ = hookpacks.RegisterCompletionHookPack(reg, noopCompletionGate{}, noopJobResolver{})
	return reg
}

// completionGateMissingFromAnInstalledSettingsFile reads the CC harness's
// real settings file (plugins/claude.HostPaths) and reports true only
// when that file exists, is readable, and does not name the
// completion-gate hook's RPC method. A missing/unreadable file (no
// harness installed, or the environment could not be resolved) reports
// false -- not this check's fault, per the file-level Outputs note.
func completionGateMissingFromAnInstalledSettingsFile() bool {
	paths, err := claude.HostPaths()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(paths.Settings)
	if err != nil {
		return false
	}
	return !strings.Contains(string(raw), hookpacks.MethodCompletionCheck)
}

// noopCompletionGate/noopJobResolver satisfy RegisterCompletionHookPack's
// non-nil guard only. CompletionGateDoctorCheck.Run never calls either
// method (see the file-level Constraints note).
type noopCompletionGate struct{}

func (noopCompletionGate) CompletionCheck(context.Context, string) (bool, string, error) {
	return false, "", cascade.New(cascade.KindUnavailable, "doctor: completion gate not available outside the daemon")
}

type noopJobResolver struct{}

func (noopJobResolver) ResolveJob(context.Context, hookpacks.CompletionHookPayload) (string, error) {
	return "", cascade.New(cascade.KindUnavailable, "doctor: job resolver not available outside the daemon")
}
