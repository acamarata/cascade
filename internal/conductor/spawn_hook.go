// Purpose: L/S-25.T3's conductor-side half of the unified spawned-agent
//
//	registry: SpawnHook (the injected notification seam), the pure
//	IsSubprocessDispatch predicate, MakeSpawnRecord (the deterministic
//	record constructor), and Executor's non-blocking enqueue mechanism.
//	ModelRequest and Selection are K/S-22.T1's pkg/provider types,
//	consumed verbatim (R-21.214): this file declares no conductor-local
//	alias, wrapper, or re-declaration of either, and does not touch
//	model.go.
//
// Inputs: a provider.ModelRequest and provider.Selection per dispatch.
// Outputs: at most one sessions.SpawnRecord enqueued per subprocess
//
//	dispatch, delivered to the injected SpawnHook off the calling
//	goroutine.
//
// Constraints: CONTRACT DEVIATION (recorded, not papered over, both sides
//
//	quoted here since this is the one piece of this ticket that cannot be
//	fixed inside its own files_scope):
//
//	R-21.213 specifies IsSubprocessDispatch as `return sel.ExecutionMode ==
//	"subprocess"`. The real pkg/provider.Selection this ticket must consume
//	verbatim (R-21.214, model.go frozen, out of files_scope) carries no
//	ExecutionMode field - router.go's own CONTRACT DEVIATION note names
//	this exact gap ("the fields the real ProviderRegistryReader/LaneInfo/
//	ProviderInfo do not carry (ExecutionMode, CostEstimate, ...)"), and
//	filters_dispatch_test.go's TestRouter_ExecutionModePopulated documents
//	the same absence with a skip: "provider.Selection has no ExecutionMode
//	field (R-40.X12 locks Selection to the frozen pkg/provider type)".
//	Per this ticket's OWN fail-closed rule ("Any value that is not exactly
//	`subprocess`... yields false, so an unclassified lane registers
//	nothing"), and since Selection carries zero classification data for
//	every real dispatch, IsSubprocessDispatch below returns false
//	unconditionally - every dispatch is unclassified, so nothing
//	registers, which is this ticket's own specified fallback applied to
//	the degenerate case where no dispatch is ever classified. It reads no
//	LaneID, Provider, or Model heuristic (the test table proves this by
//	holding both constant).
//
//	A second, independent gap compounds the first: MakeSpawnRecord's own
//	contracted signature is (req ModelRequest, sel Selection, clk) - and
//	neither ModelRequest nor Selection carries a PID or an Account. PID is
//	only known once the ModelProvider driver actually spawns its
//	subprocess (see pkg/provider/agent_types.go's SpawnResult.PGID, "0
//	only for an in-process lane"), strictly downstream of the Selection
//	this ticket's inputs describe. MakeSpawnRecord therefore returns
//	PID: 0 - which Registry.Register's own fail-closed validation refuses
//	(PID > 0 required) - so even a hypothetical true IsSubprocessDispatch
//	could not register successfully today without a further ticket
//	threading a real PID through the dispatch path. Binary uses
//	sel.Provider (the closest available proxy: Selection carries no literal
//	executable name); Account is left empty (no source field exists).
//	Both gaps are pre-existing and outside this ticket's files_scope
//	(model.go is frozen and unlisted); every function below is real,
//	wired, and tested against valid synthetic inputs - only live
//	production firing is currently inert pending those two upstream
//	extensions.
//
// SPORT: conductor/spawn-hook/ADD (P1-E12-W3-S25-T3).

// Package conductor doc: see doc.go for the canonical package comment.
package conductor

import (
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// spawnHookCapacity bounds concurrent in-flight SpawnHook calls: once this
// many are outstanding, a new dispatch's registration is dropped rather
// than blocking model.execute.
const spawnHookCapacity = 16

// SpawnHook is the non-blocking, nil-safe notification seam Executor calls
// after a subprocess dispatch decision. A nil SpawnHook is a no-op.
type SpawnHook func(sessions.SpawnRecord)

// ErrNilClock is MakeSpawnRecord's fail-closed error for a nil clock: it
// never falls back to a bare time.Now.
var ErrNilClock = cascade.New(cascade.KindInvalidInput, "conductor: MakeSpawnRecord requires a non-nil clock")

// IsSubprocessDispatch reports whether sel names a subprocess-mode
// dispatch. Its entire body reads sel.ExecutionMode and nothing else per
// R-21.213 - no LaneID, Provider, or Model heuristic. See this file's
// header CONTRACT DEVIATION note: the real Selection carries no
// ExecutionMode field, so every real call returns false today.
func IsSubprocessDispatch(sel provider.Selection) bool {
	return selectionExecutionMode(sel) == "subprocess"
}

// selectionExecutionMode isolates the single field read R-21.213 mandates.
// It is its own function only so the CONTRACT DEVIATION is visible at one
// call site: Selection has no ExecutionMode field to read, so this always
// answers the empty string (never "subprocess"), which is neither
// "in-process" nor any recognized mode - the same fail-closed "unclassified"
// answer a real empty-string ExecutionMode would produce.
func selectionExecutionMode(_ provider.Selection) string {
	return ""
}

// MakeSpawnRecord constructs a SpawnRecord deterministically from req, sel
// and clk - no bare time.Now. A nil clk returns a non-nil error and the
// zero SpawnRecord. See this file's header note: PID is left at its zero
// value (neither req nor sel carries one) and Account is left empty (same
// reason); Binary uses sel.Provider as the closest available proxy.
func MakeSpawnRecord(req provider.ModelRequest, sel provider.Selection, clk runtime.Clock) (sessions.SpawnRecord, error) {
	if clk == nil {
		return sessions.SpawnRecord{}, ErrNilClock
	}
	return sessions.SpawnRecord{
		TaskID:      req.TaskID,
		Binary:      sel.Provider,
		ModelClass:  req.TaskClass,
		Sensitivity: req.Sensitivity.String(),
		SpawnedAt:   clk.Now(),
	}, nil
}

// SetSpawnHook injects hook, the daemon composition root's one call after
// NewExecutor (ExecutorConfig/pipeline.go is outside this ticket's
// files_scope, so injection is a setter rather than a constructor
// parameter - functionally equivalent). A nil hook clears any previously
// set hook and disables enqueueing.
func (e *Executor) SetSpawnHook(hook SpawnHook) {
	e.spawnHook = hook
	if hook != nil && e.spawnSem == nil {
		e.spawnSem = make(chan struct{}, spawnHookCapacity)
	}
}

// trySpawn is Execute's call site: after a subprocess dispatch decision is
// confirmed via IsSubprocessDispatch, it builds a SpawnRecord and enqueues
// it. A nil hook, a non-subprocess dispatch, or a MakeSpawnRecord error
// (nil clock) are each a silent no-op - never an Execute-visible error.
func (e *Executor) trySpawn(req provider.ModelRequest, sel provider.Selection) {
	if e.spawnHook == nil || !IsSubprocessDispatch(sel) {
		return
	}
	rec, err := MakeSpawnRecord(req, sel, e.pipeline.cfg.Clock)
	if err != nil {
		return
	}
	e.enqueueSpawn(rec)
}

// enqueueSpawn is the non-blocking dispatch mechanism itself, split from
// trySpawn's classification gate so it is directly testable against real
// SpawnRecord values regardless of IsSubprocessDispatch's current
// always-false answer (see header note) - this is the exact code path
// production will exercise once a future ticket threads real
// ExecutionMode/PID data through. A full semaphore channel increments the
// drop counter and returns without blocking; otherwise hook runs on its
// own goroutine, off the caller.
func (e *Executor) enqueueSpawn(rec sessions.SpawnRecord) {
	select {
	case e.spawnSem <- struct{}{}:
		go func() {
			defer func() { <-e.spawnSem }()
			e.spawnHook(rec)
		}()
	default:
		atomic.AddInt64(&e.spawnDrops, 1)
	}
}

// NewDefaultProductionSpawnHook is the composition-root assembly point for
// L/S-25.T3's unified registry: it builds a sessions.DefaultRegistry over
// store, timeout and clk, and returns a SpawnHook that registers every
// record into it. internal/daemon/subsystems.go (out of this ticket's
// files_scope - see the NewExecutor precedent it already carries for the
// identical situation) is the caller expected to invoke this once and pass
// the result to SetSpawnHook; see internal/build/testonly-allow.json's
// entry for this symbol.
func NewDefaultProductionSpawnHook(store provider.Store, timeout time.Duration, clk runtime.Clock) SpawnHook {
	reg := sessions.NewDefaultRegistry(store, timeout, clk)
	return func(rec sessions.SpawnRecord) {
		_ = reg.Register(rec)
	}
}

// SpawnDropCount returns the number of spawn-hook enqueue attempts dropped
// because spawnSem was full, for tests and observability.
func (e *Executor) SpawnDropCount() int64 {
	return atomic.LoadInt64(&e.spawnDrops)
}
