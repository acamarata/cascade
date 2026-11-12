//go:build !windows

// Purpose: the daemon half of the composition root's registration contract.
//
//	Every daemon subsystem is one file, wire_<subsystem>.go, holding one
//	registerDaemonWiring call. buildDaemonRegistry builds the registry and
//	the events mux, then runDaemonWiring runs the registrations in
//	(Phase, Order, Name) order and nothing else.
//
// Inputs:  daemonRegistration values registered from package-level
//
//	initialisers, and the daemonWiring a daemon build hands each of them.
//
// Outputs: a fully wired *rpc.Registry and events mux.
// Constraints: daemon_unix_run.go is never edited to add a subsystem. A value
//
//	platformDaemonRun holds that a subsystem needs is read from w.Deps; adding
//	one is a single-hunk edit of daemonRuntime here, not a new rpcServerOption.
//	phaseMiddleware is empty until the elevation middleware lands there.
//	Exactly one registration uses phaseMCPLast so it sees the finished method
//	table. Pre-serve wiring (recovery scan, background subsystems, upgrade)
//	stays in composeDaemon; the doctor check registry stays a hand-kept list.
//
// SPORT: cmd/cascade composition root — daemon registrations (P1-CORE-01).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// daemonPhase orders registrations: a lower phase runs first.
type daemonPhase int

// The phases, in the order they run.
const (
	phaseMiddleware  daemonPhase = 100
	phaseCore        daemonPhase = 200
	phaseNamespaces  daemonPhase = 300
	phaseConductor   daemonPhase = 400
	phaseSupervised  daemonPhase = 500
	phaseLate        daemonPhase = 600
	phaseOptions     daemonPhase = 700
	phaseFleet       daemonPhase = 800
	phaseEventTopics daemonPhase = 850
	phaseMCPLast     daemonPhase = 900
)

// daemonWiring is what every registration receives. Manifest and Connections
// are produced by the status registration (phaseCore, the first non-middleware
// registration), so every later registration reads them non-nil.
type daemonWiring struct {
	Ctx         context.Context
	Registry    *rpc.Registry
	Manifest    *daemon.Manifest
	Connections *int64
	Events      *rpc.SSEMux
	Bus         *events.Bus
	Clock       runtime.Clock
	Logger      *slog.Logger
	Settings    daemon.Settings
	Paths       runtime.PathProvider
	Store       provider.Store
	MemoryAdmin *memory.AdminHandler
	Opts        []rpcServerOption
	Deps        daemonRuntime
	// ConductorFanOut is the conductor registration's one per-daemon fan-out
	// (leg budget and claim table), read by the resume registration.
	ConductorFanOut daemon.ConductorFanOut
}

// daemonRuntime is everything platformDaemonRun holds beyond daemonWiring's
// other fields. A test-built wiring (no composeDaemon) leaves it zero.
type daemonRuntime struct {
	// Evidence is the completion gate's sole writer ledger, shared with reads.
	Evidence    *jobs.EvidenceLedger
	Config      *runtime.Config
	RawDB       *sql.DB
	Policy      *policyWiring
	LogProvider *runtime.LogProvider
	Daemon      daemonDeps
	// Notify is the one notification runtime; the notify registration
	// (phaseSupervised, Order 5) sets it for every later registration.
	Notify *daemon.NotifyRuntime
	// Presence is the operator-presence tracker; the presence registration
	// (phaseMiddleware, Order 20) sets it for every later registration.
	Presence *daemon.PresenceTracker
	// ElevationVerifier recognises (and cannot mint) the witnesses the one
	// elevation middleware issues; the elevation registration (phaseMiddleware,
	// Order 10) sets it. The zero value matches nothing.
	ElevationVerifier rpc.WitnessVerifier
	// ConfigBaseline is the boot baseline check's outcome and the
	// config.accept_loosening state (daemon_unix_config_baseline.go).
	ConfigBaseline *configBaseline
	// Fleet is the daemon's one fleet runtime (admission controller and the
	// sampler behind it); composeDaemon builds it before any registration.
	Fleet *daemon.FleetRuntime
	// CI is the daemon's one CI runtime (the streaming dispatcher); the "ci"
	// registration (wire_ci.go) sets it.
	CI *daemon.CIRuntime
	// Reloader is the daemon's one config HotReloader; Current() is the
	// effective config every live reader reads.
	Reloader *runtime.HotReloader
	// ConductorPolicy is the daemon's one quota policy and owner deny store
	// (conductor_deny_store.go). The conductor wiring and the reservation
	// owner (P1-TOP-04) read these same two values; neither builds its own.
	ConductorPolicy *conductorPolicy
	// Creds is the start-time credential snapshot (P1-GWY-10); registrations
	// read gateway and credential values through Creds.Getenv only.
	Creds *credentialEnv
	// Minter is the daemon's one principal minter; the principal registration
	// (phaseMiddleware, Order 5) sets it, so every token a later registration
	// mints resolves through the same admission policy.
	Minter *principal.Minter
}

// Reconcile brings the snapshot in line with names, the credentialNames of a
// newly accepted config, under one write lock: every newly named name is
// snapshotted from the process environment, unset and admitted, and every
// entry whose name names no longer lists is deleted, so a removed gateway
// loses its credential at once. Names and values are never logged here.
func (c *credentialEnv) Reconcile(names []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keep := make(map[string]bool, len(names))
	var fresh []string
	for _, n := range names {
		if n == "" {
			continue
		}
		keep[n] = true
		if !c.scrubbed[n] {
			fresh = append(fresh, n)
		}
	}
	c.scrubLocked(fresh, os.LookupEnv, os.Unsetenv)
	for _, n := range fresh {
		c.admitted[n] = true
	}
	for n := range c.scrubbed {
		if !keep[n] {
			delete(c.scrubbed, n)
			delete(c.admitted, n)
			delete(c.values, n)
		}
	}
}

// conductorPolicy is built once in composeDaemon over Reloader.Current();
// start refuses when either value does not parse. Quota is re-applied on
// every accepted reload (QuotaPolicy.Apply); Deny reads the live config on
// every call.
type conductorPolicy struct {
	Quota *conductor.QuotaPolicy
	Deny  conductor.DenyStore
}

// daemonRegistration is one subsystem's wiring step. Name must be unique and
// is what a failed Wire is reported under.
type daemonRegistration struct {
	Name  string
	Phase daemonPhase
	Order int
	Wire  func(w *daemonWiring) error
}

// daemonRegistrations holds every registration, in registration order.
// runDaemonWiring sorts a copy, so init order never matters.
var daemonRegistrations []daemonRegistration

// registerDaemonWiring records r. It is used only as
// `var _ = registerDaemonWiring(daemonRegistration{...})` in a wire_<subsystem>.go file.
func registerDaemonWiring(r daemonRegistration) struct{} {
	daemonRegistrations = append(daemonRegistrations, r)
	return struct{}{}
}

// validateDaemonRegistrations reports the first duplicate Name, duplicate
// (Phase, Order), second phaseMCPLast registration or nil Wire in regs.
func validateDaemonRegistrations(regs []daemonRegistration) error {
	names := make(map[string]bool, len(regs))
	slots := make(map[[2]int]string, len(regs))
	mcpLast := ""
	for _, r := range regs {
		slot := [2]int{int(r.Phase), r.Order}
		switch {
		case r.Name == "" || r.Wire == nil:
			return cascade.Newf(cascade.KindInternal, "daemon registration %q: empty name or nil Wire", r.Name)
		case names[r.Name]:
			return cascade.Newf(cascade.KindInternal, "duplicate daemon registration name %q", r.Name)
		case slots[slot] != "":
			return cascade.Newf(cascade.KindInternal, "daemon registrations %q and %q share phase %d order %d", slots[slot], r.Name, r.Phase, r.Order)
		case r.Phase == phaseMCPLast && mcpLast != "":
			return cascade.Newf(cascade.KindInternal, "daemon registrations %q and %q both use phaseMCPLast", mcpLast, r.Name)
		}
		names[r.Name], slots[slot] = true, r.Name
		if r.Phase == phaseMCPLast {
			mcpLast = r.Name
		}
	}
	return nil
}

// sortedDaemonRegistrations returns a copy of regs sorted by (Phase, Order, Name).
func sortedDaemonRegistrations(regs []daemonRegistration) []daemonRegistration {
	out := append([]daemonRegistration(nil), regs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Name < b.Name
	})
	return out
}

// runDaemonWiring runs every registration sorted by (Phase, Order, Name). It
// stops at the first error and returns it wrapped with the entry's Name, the
// cause preserved; no later registration runs. On success it records the
// registry's method count in the manifest.
func runDaemonWiring(w *daemonWiring) error {
	if err := validateDaemonRegistrations(daemonRegistrations); err != nil {
		return err
	}
	for _, r := range sortedDaemonRegistrations(daemonRegistrations) {
		if err := r.Wire(w); err != nil {
			kind, ok := cascade.KindOf(err)
			if !ok {
				kind = cascade.KindInternal
			}
			return cascade.Wrapf(kind, err, "daemon wiring %q", r.Name)
		}
	}
	w.Manifest.Started("rpc-registry", fmt.Sprintf("%d methods", len(w.Registry.Methods())))
	return nil
}

// newDaemonEventsMux builds the GET /events mux with its default (no-topic)
// leg: the daemon-wide SSEHandler bound to exactly ONE bus namespace,
// "daemon", the only namespace this composition root publishes daemon-wide
// events to (upgrade.go's EventKindShutdownRequested, plus the job-lease,
// supervisor and status-widget kinds). Topics are added by event-topics
// registrations. FAIL-CLOSED: any other topic shape is refused by the mux,
// never served the default stream (internal/rpc/sse_mux.go).
func newDaemonEventsMux(bus *events.Bus, clock runtime.Clock) *rpc.SSEMux {
	knownEventKind := rpc.CombineKnownEventKind(func(kind events.EventKind) bool {
		return kind == daemon.EventKindShutdownRequested
	}, rpc.KnownJobLeaseEventKind, rpc.KnownSupervisorEventKind, daemon.KnownStatusWidgetEventKind)
	return rpc.NewSSEMux(rpc.NewSSEHandler(bus, "daemon", knownEventKind, clock))
}
