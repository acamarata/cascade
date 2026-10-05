// Purpose: the doctor checks that read the LIVE daemon: the subsystem census,
//
//	the dropped-hook-event warning and the storage probe, all named inside
//	productionCheckRegistry. Every one of them answers from a status.get
//	call (or, for storage, the database file the daemon owns), so each is
//	gated by daemonStateCheck: a daemon that does not answer is a warning,
//	never a pass.
//
// Inputs: a daemonStatusSource (status.get over the daemon socket, asked at
//
//	most once per doctor run) and the PathProvider for the data directory.
//
// Outputs: doctor.Checks named subsystem_census, hook-events and storage.
// Constraints: the census counts a subsystem only when its state is neither
//
//	disabled nor skipped (an operator's choice or a disclosed precondition,
//	not a failure); those two are listed in Detail so they stay visible.
//	The storage check is a FILTER like --harness: the default report leaves
//	it out (it writes a sentinel row), `doctor --storage` runs only it. A
//	held exclusive lock is the running daemon's own, so it is expected
//	while status.get answers and a conflict when nothing does.
//
// SPORT: cmd/cascade/doctor (ADD, daemon-state checks).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// storageCheckName is the registry name `doctor --storage` selects and the
// default report leaves out.
const storageCheckName = "storage"

// daemonStatusSource answers status.get. An error means no daemon answered.
type daemonStatusSource func(ctx context.Context) (daemon.StatusResponse, error)

// daemonStatusSourceFor dials the daemon socket config.toml resolves and
// asks status.get, once: the first answer (or failure) is kept for the rest
// of the run, so three checks cost one round trip and agree with each other.
func daemonStatusSourceFor(paths runtime.PathProvider) daemonStatusSource {
	var (
		once sync.Once
		resp daemon.StatusResponse
		err  error
	)
	return func(ctx context.Context) (daemon.StatusResponse, error) {
		once.Do(func() {
			deps := statusDeps{Paths: paths, Getenv: os.Getenv, Environ: os.Environ, DialContext: client.UnixDialer}
			var settings daemon.Settings
			if settings, err = resolveStatusSocket(ctx, deps); err != nil {
				return
			}
			resp, err = client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout).Status(ctx)
		})
		return resp, err
	}
}

// daemonStateCheck gates a check that needs a live daemon. Name, Describe,
// Metadata and Fix are the wrapped check's.
type daemonStateCheck struct {
	doctor.Check
	status daemonStatusSource
}

// Run warns "daemon not running" without calling the wrapped check when
// status.get does not answer: its subject cannot be read, which is neither
// healthy nor proven broken.
func (c daemonStateCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if _, err := c.status(ctx); err != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     "daemon not running",
			Detail:      err.Error(),
			Remediation: "start it with `cascade daemon start`; this check reads the live daemon",
		}, nil
	}
	return c.Check.Run(ctx)
}

// statusCensus is the doctor.SubsystemStateProvider over status.get.
type statusCensus struct{ status daemonStatusSource }

// DeclaredSubsystems lists the subsystems the census holds to account.
func (p statusCensus) DeclaredSubsystems(ctx context.Context) ([]string, error) {
	resp, err := p.status(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range resp.Subsystems {
		if s.State != daemon.SubsystemDisabled && s.State != daemon.SubsystemSkipped {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// RunningSubsystems reports which subsystems status.get says are running.
func (p statusCensus) RunningSubsystems(ctx context.Context) (map[string]bool, error) {
	resp, err := p.status(ctx)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, s := range resp.Subsystems {
		running[s.Name] = s.State == daemon.SubsystemRunning
	}
	return running, nil
}

// censusWithExclusions adds the subsystems the census did not count
// (disabled, skipped) to the wrapped result's Detail.
type censusWithExclusions struct {
	doctor.Check
	status daemonStatusSource
}

// Run runs the census and appends what it left out.
func (c censusWithExclusions) Run(ctx context.Context) (doctor.CheckResult, error) {
	res, err := c.Check.Run(ctx)
	resp, serr := c.status(ctx)
	if err != nil || serr != nil {
		return res, err
	}
	var parts []string
	for _, state := range []daemon.SubsystemState{daemon.SubsystemDisabled, daemon.SubsystemSkipped} {
		var names []string
		for _, s := range resp.Subsystems {
			if s.State == state {
				names = append(names, s.Name)
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			parts = append(parts, fmt.Sprintf("%s: %s", state, strings.Join(names, ", ")))
		}
	}
	if len(parts) > 0 {
		note := "not counted (" + strings.Join(parts, "; ") + ")"
		res.Detail = strings.TrimPrefix(res.Detail+"; "+note, "; ")
	}
	return res, nil
}

// newCensusDoctorCheck wraps the census (built by the caller, over
// statusCensus) so it lists what it left out and is daemon-gated.
func newCensusDoctorCheck(census doctor.Check, status daemonStatusSource) doctor.Check {
	return daemonStateCheck{Check: censusWithExclusions{Check: census, status: status}, status: status}
}

// hookEventsCheck warns when the daemon has dropped hook events whose type
// it does not recognise.
type hookEventsCheck struct{ status daemonStatusSource }

func (hookEventsCheck) Name() string { return "hook-events" }
func (hookEventsCheck) Describe() string {
	return "the daemon has not dropped hook events of a type it does not recognise"
}
func (hookEventsCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }
func (hookEventsCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run reads hooks.unknown_event_drops from status.get.
func (c hookEventsCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	resp, err := c.status(ctx)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "could not read hook event state", Detail: err.Error()}, nil
	}
	n := resp.Hooks.UnknownEventDrops
	if n > 0 {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     fmt.Sprintf("%d hook event(s) dropped: unknown event type", n),
			Detail:      "counted since the daemon started",
			Remediation: "upgrade the daemon: a harness is sending hook events this version does not recognise",
		}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: "no hook events dropped"}, nil
}

// newHookEventsDoctorCheck builds the dropped-hook-event check.
func newHookEventsDoctorCheck(status daemonStatusSource) doctor.Check {
	return daemonStateCheck{Check: hookEventsCheck{status: status}, status: status}
}

// storageDoctorCheck probes cascade.db with storage.StorageHealthCheck.
type storageDoctorCheck struct {
	dataDir string
	status  daemonStatusSource
}

// newStorageDoctorCheck builds the storage check. It is not daemon-gated:
// the database is probed whether or not a daemon runs.
func newStorageDoctorCheck(paths runtime.PathProvider, status daemonStatusSource) doctor.Check {
	return storageDoctorCheck{dataDir: paths.DataDir(), status: status}
}

func (storageDoctorCheck) Name() string { return storageCheckName }
func (storageDoctorCheck) Describe() string {
	return "cascade.db: WAL mode, schema version, domain tables, a write round trip and the exclusive lock"
}
func (storageDoctorCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }
func (storageDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run opens the existing database (mode=rw never creates one) and runs the
// five storage probes against it.
func (c storageDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.dataDir == "" {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "could not resolve the cascade data directory"}, nil
	}
	path := filepath.Join(c.dataDir, "cascade.db")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return doctor.CheckResult{Status: doctor.StatusWarn, Message: "no cascade.db yet", Detail: path,
			Remediation: "start the daemon once: it creates and migrates the database"}, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_busy_timeout=5000")
	if err == nil {
		defer func() { _ = db.Close() }()
		err = db.PingContext(ctx)
	}
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "could not open cascade.db", Detail: err.Error()}, nil
	}
	_, daemonErr := c.status(ctx)
	return storageResult(storage.StorageHealthCheck(ctx, db), daemonErr == nil), nil
}

// storageResult folds a storage.HealthReport into one CheckResult: every
// failing probe is named in Detail and any failure is StatusError. The lock
// probe's conflict is the daemon's own lock when the daemon answered, so it
// is not a failure then.
func storageResult(report storage.HealthReport, daemonUp bool) doctor.CheckResult {
	results := report.Results()
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	var failed []string
	for _, name := range names {
		r := results[name]
		if r.OK || (name == "flock-probe" && daemonUp && cascade.HasKind(r.Err, cascade.KindConflict)) {
			continue
		}
		failed = append(failed, name+": "+r.Detail)
	}
	if len(failed) > 0 {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     fmt.Sprintf("%d of %d storage probe(s) failed", len(failed), len(names)),
			Detail:      strings.Join(failed, "; "),
			Remediation: "stop the daemon and restore cascade.db from a backup, or file a defect with this report",
		}
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: fmt.Sprintf("%d/%d storage probes passed", len(names), len(names))}
}

// withoutStorageCheck drops the storage check from the default run.
func withoutStorageCheck(checks []doctor.Check) []doctor.Check {
	kept := make([]doctor.Check, 0, len(checks))
	for _, c := range checks {
		if c.Name() != storageCheckName {
			kept = append(kept, c)
		}
	}
	return kept
}

// onlyStorageCheck narrows a run to the storage check; a registry without
// one yields nothing, which executeChecks refuses rather than reporting a
// clean empty run.
func onlyStorageCheck(reg *doctor.CheckRegistry) []doctor.Check {
	check, ok := reg.Lookup(storageCheckName)
	if !ok {
		return nil
	}
	return []doctor.Check{check}
}
