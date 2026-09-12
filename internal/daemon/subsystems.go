package daemon

// Purpose: the declared-subsystem manifest (R-14.87 fail-loud subsystems):
//   daemon bootstrap registers every subsystem it EXPECTS to start before
//   attempting any of them, so "this subsystem's log line never appeared"
//   is always distinguishable from "this subsystem was never declared" —
//   absence of a log line is never the only evidence of a subsystem's
//   state. At W1 the only subsystem Run registers is the IPC socket
//   listener; later tickets (event bus, scheduler, SSE bridge, MCP socket
//   transport, probes) call Register/Started/Failed against this same
//   Manifest as they land.
// Inputs: a *slog.Logger and runtime.Clock, injected once at construction
//   (Art.7.1 — no bare time.Now, R-14.11).
// Outputs: a structured INFO line on every subsystem's success and a
//   distinct ERROR-level line on disabled/skipped/failed, plus Snapshot(),
//   the accessor D/S-07.T1's status.get handler and the doctor
//   subsystem_census check read.
// Constraints: Art.10.3 — split from daemon.go/lifecycle_unix.go purely to
//   keep each file under the 300-line cap; same package, no import cycle
//   risk. Concurrency-safe: Run's accept loop and its signal-handling
//   goroutine may both touch the manifest.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// SubsystemState is one subsystem's lifecycle state as tracked by Manifest.
type SubsystemState int

const (
	// SubsystemDeclared reports that Register was called; the subsystem has
	// not yet attempted to start.
	SubsystemDeclared SubsystemState = iota
	// SubsystemRunning reports that the subsystem started successfully.
	SubsystemRunning
	// SubsystemError reports that the subsystem failed to bind/start.
	SubsystemError
	// SubsystemDisabled reports that the subsystem is intentionally not
	// started (e.g. config-gated off).
	SubsystemDisabled
	// SubsystemSkipped reports that the subsystem was bypassed for a reason
	// other than explicit disablement (e.g. a platform refusal).
	SubsystemSkipped
)

// String renders the state for structured log fields and Snapshot JSON.
func (s SubsystemState) String() string {
	switch s {
	case SubsystemDeclared:
		return "declared"
	case SubsystemRunning:
		return "running"
	case SubsystemError:
		return "error"
	case SubsystemDisabled:
		return "disabled"
	case SubsystemSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// SubsystemStatus is one Manifest entry's exported snapshot row.
type SubsystemStatus struct {
	Name      string         `json:"name"`
	State     SubsystemState `json:"state"`
	Detail    string         `json:"detail"`
	UpdatedAt string         `json:"updated_at"`
}

// Manifest is the fail-loud declared-subsystem registry. The zero value is
// not usable; construct with NewManifest.
type Manifest struct {
	mu      sync.Mutex
	log     *slog.Logger
	clock   runtime.Clock
	entries map[string]*SubsystemStatus
	order   []string
}

// NewManifest builds a Manifest. A nil logger discards log lines (tests
// that only care about Snapshot state may pass nil); a nil clock falls back
// to runtime.NewSystemClock() — production callers should always inject
// the real one explicitly so the fallback is visible at the call site.
func NewManifest(log *slog.Logger, clock runtime.Clock) *Manifest {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Manifest{log: log, clock: clock, entries: map[string]*SubsystemStatus{}}
}

// Register declares name as an expected subsystem, in state Declared,
// before any attempt to start it. Calling Register twice for the same name
// is a no-op against the existing entry (idempotent declaration).
func (m *Manifest) Register(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; exists {
		return
	}
	m.entries[name] = &SubsystemStatus{Name: name, State: SubsystemDeclared, UpdatedAt: m.now()}
	m.order = append(m.order, name)
}

// Started records name's successful start at addrOrState (e.g. a socket
// path, a listen address) and logs a structured INFO start line.
func (m *Manifest) Started(name, addrOrState string) {
	m.set(name, SubsystemRunning, addrOrState)
	m.logf(slog.LevelInfo, name, "subsystem started", addrOrState)
}

// Failed records name's bind/start failure and logs a distinct ERROR-level
// line — the fail-loud half of R-14.87.
func (m *Manifest) Failed(name, reason string) {
	m.set(name, SubsystemError, reason)
	m.logf(slog.LevelError, name, "subsystem failed to start", reason)
}

// Disabled records that name was intentionally not started and logs an
// ERROR-level line (R-14.87: disabled is one of the three states that must
// never be silent).
func (m *Manifest) Disabled(name, reason string) {
	m.set(name, SubsystemDisabled, reason)
	m.logf(slog.LevelError, name, "subsystem disabled", reason)
}

// Skipped records that name was bypassed (e.g. a platform refusal) and
// logs an ERROR-level line, same rationale as Disabled.
func (m *Manifest) Skipped(name, reason string) {
	m.set(name, SubsystemSkipped, reason)
	m.logf(slog.LevelError, name, "subsystem skipped", reason)
}

// Snapshot returns every registered subsystem's current status, in
// registration order — the accessor D/S-07.T1's status.get handler and the
// doctor subsystem_census check use.
func (m *Manifest) Snapshot() []SubsystemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SubsystemStatus, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, *m.entries[name])
	}
	return out
}

func (m *Manifest) set(name string, state SubsystemState, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, exists := m.entries[name]
	if !exists {
		e = &SubsystemStatus{Name: name}
		m.entries[name] = e
		m.order = append(m.order, name)
	}
	e.State = state
	e.Detail = detail
	e.UpdatedAt = m.now()
}

func (m *Manifest) now() string {
	return m.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (m *Manifest) logf(level slog.Level, name, msg, detail string) {
	if m.log == nil {
		return
	}
	m.log.Log(context.Background(), level, msg, slog.String("subsystem", name), slog.String("detail", detail))
}

// conductorRouterSubsystem is the fail-loud Manifest name
// RegisterConductorRouter reports under (R-14.87).
const conductorRouterSubsystem = "conductor.router"

// RegisterConductorRouter is the daemon composition root's call site for
// wiring internal/conductor/task_classes.go's §5.16 taxonomy table into a
// conductor.DefaultRouter (R-14.38, R-21.217). It builds the router from
// reg/quota/clock — the same registry and QuotaPolicy dependencies
// R-21.217 says already live at the composition root — plus
// conductor.TaskClasses(), and records the wiring against m so "the
// taxonomy table never reached the router" is a distinguishable, logged
// failure rather than silent absence (R-14.87).
//
// CONTRACT DEVIATION (recorded, not papered over): this ticket's contract
// text asserts subsystems.go "is where the Router's registry and
// QuotaPolicy dependencies already live". They do not: at the time this
// ticket landed, no file under internal/daemon constructed a
// conductor.Router, a conductor.Executor, or any registry/QuotaPolicy
// instance — grep across internal/daemon for NewDaemonRouter, NewRouter
// and NewExecutor returns zero production call sites. This function is
// therefore the composition root's FIRST Router-construction call site,
// not a pre-existing one gaining a new argument, and no daemon startup
// path (daemon.go, lifecycle_unix.go) yet calls it: those files are
// outside this ticket's files_scope (internal/daemon/subsystems.go only).
// See task_classes.go's journal entry for both sides quoted, and this
// file's own testonly-allow.json entry recording the same gap.
func (m *Manifest) RegisterConductorRouter(reg provider.ProviderRegistryReader, quota conductor.QuotaSpiller, clock conductor.Clock) (*conductor.DefaultRouter, error) {
	m.Register(conductorRouterSubsystem)
	classes := conductor.TaskClasses()
	if len(classes) != 9 {
		reason := fmt.Sprintf("task-class taxonomy table has %d rows, want 9", len(classes))
		m.Failed(conductorRouterSubsystem, reason)
		return nil, fmt.Errorf("daemon: %s: %s", conductorRouterSubsystem, reason)
	}
	router := conductor.NewRouter(reg, quota, clock, classes)
	m.Started(conductorRouterSubsystem, fmt.Sprintf("%d task classes loaded", len(classes)))
	return router, nil
}

// reachabilitySubsystem is the fail-loud Manifest name RegisterReachability
// reports under (R-14.87).
const reachabilitySubsystem = "jobs.reachability"

// reachabilityClasses fixes the R-21.182 sensitive-class set the
// AC/S-59.T4 footprint rule's reachability expansion checks: auth, secret
// and schema, exactly as R-21.182's own text names them.
var reachabilityClasses = []repo.SensitiveClass{repo.ClassAuth, repo.ClassSecret, repo.ClassSchema}

// RegisterReachability is the daemon composition root's call site for
// wiring AG/S-67.T3's internal/repo.Reachable into the internal/jobs
// ReachabilityFn seam settled by R-21.257: AC/S-59.T4 declares
// `func(ctx context.Context, paths []string) ([]string, error)` without
// importing a graph package, and this closure is the ONLY adapter --
// Reachable keeps its own ctx/paths/classes parameters and error return,
// and the seam type never imports internal/repo. graph must be non-nil.
//
// CONTRACT DEVIATION (recorded, not papered over -- matches
// RegisterConductorRouter's own precedent above): no daemon startup path
// (daemon.go, lifecycle_unix.go) yet calls RegisterReachability or
// jobs.NewPlanner -- grep across internal/daemon for both returns zero
// other production call sites at the time this ticket landed. This is
// the composition root's FIRST call site for the reachability seam, the
// same posture RegisterConductorRouter recorded for the router seam; a
// later ticket wiring the scheduler (AH/S-69.T3) or the footprint rule
// itself (AC/S-59.T4) is the one that calls it from a real startup path.
func (m *Manifest) RegisterReachability(graph *repo.SymbolGraph) (jobs.ReachabilityFn, error) {
	m.Register(reachabilitySubsystem)
	reach, err := repo.NewReachability(graph)
	if err != nil {
		m.Failed(reachabilitySubsystem, err.Error())
		return nil, err
	}
	fn := func(ctx context.Context, paths []string) ([]string, error) {
		return reach.Reachable(ctx, paths, reachabilityClasses)
	}
	m.Started(reachabilitySubsystem, fmt.Sprintf("%d sensitive classes wired", len(reachabilityClasses)))
	return fn, nil
}

// worktreeSweepSubsystem is the fail-loud Manifest name
// RegisterWorktreeSweep reports under (R-14.87).
const worktreeSweepSubsystem = "jobs.worktree.sweep"

// RegisterWorktreeSweep is the daemon composition root's call site for
// AC/S-59.T3's HOW step 5: it constructs the WorktreeManager (the real
// jobs.NewWorktreeManager production call site) and runs its Sweep once,
// synchronously, AT subsystem startup (R-21.140/R-21.177's daemon-start
// orphan reconciliation), recording the outcome against m -- the exact
// RegisterConductorRouter/RegisterReachability posture above. The
// returned *jobs.WorktreeManager is the SAME instance
// RegisterWorktreeManager (subsystems_worktree.go) then wires to the
// acquired/released event stream, so both halves of this ticket's daemon
// wiring share one manager.
//
// CONTRACT DEVIATION (recorded, matching both precedents above): no
// daemon startup path (daemon.go, lifecycle_unix.go) yet calls
// RegisterWorktreeSweep -- out of this ticket's files_scope. See
// subsystems_worktree.go's RegisterWorktreeManager for the companion
// event-wiring call site, split out purely for the 300-line cap.
func (m *Manifest) RegisterWorktreeSweep(ctx context.Context, store *jobs.Store, j journal.Store, attn *supervision.Store, probe jobs.ProcessLivenessProbe) (*jobs.WorktreeManager, jobs.SweepResult, error) {
	m.Register(worktreeSweepSubsystem)
	wt := jobs.NewWorktreeManager(store, j, attn, probe)
	result, err := wt.Sweep(ctx)
	if err != nil {
		m.Failed(worktreeSweepSubsystem, err.Error())
		return wt, result, err
	}
	m.Started(worktreeSweepSubsystem, fmt.Sprintf("%d removed, %d quarantined, %d pruned",
		len(result.Removed), len(result.Quarantined), len(result.Pruned)))
	return wt, result, nil
}
