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
	"sort"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
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
}

// daemonRuntime is everything platformDaemonRun holds beyond daemonWiring's
// other fields. A test-built wiring (no composeDaemon) leaves it zero.
type daemonRuntime struct {
	Config      *runtime.Config
	RawDB       *sql.DB
	Policy      *policyWiring
	LogProvider *runtime.LogProvider
	Daemon      daemonDeps
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
