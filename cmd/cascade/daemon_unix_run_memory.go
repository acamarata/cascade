//go:build !windows

// Purpose: the memory.* half of platformDaemonRun's composition wiring:
//
//	mounting the memory, soul and memory.review namespaces on the daemon's
//	RPC router, and the composition-root adapter that publishes the SOUL
//	store's divergence event on the real bus. Split out of
//	daemon_unix_run.go when that file reached 321 lines against the
//	300-line cap; this is the same composition-root wiring, divided along
//	the one seam that already had a name, not a new concern.
//
// Inputs: the rpc.Registry to mount on, a runtime.PathProvider and Clock,
//
//	the real *events.Bus, and the memory.AdminHandler (nil where no
//	scheduler was wired).
//
// Outputs: the memory.*, soul.* and memory.review.* verbs registered on
//
//	the router, and a bus event per SOUL divergence.
//
// Constraints: every dependency flows in as a parameter, as in the file
//
//	this was split from. The divergence payload carries no soul text.
//
// SPORT: cmd/cascade/daemon (ADD).
package main

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// registerMemoryHandler mounts the memory.* namespace on the daemon's RPC
// router, rooted at memoryStoreDir (cmd/cascade/memory.go). It lives in
// THIS file rather than beside the command because cmd/cascade may not
// import internal/rpc outside the three composition-root files the
// cmd-rpc-server-boundary rule exempts, and this file is one of them: a
// command that reached for the server package would be one step from
// hand-rolling its own outbound JSON-RPC, which is the thing the boundary
// exists to prevent.
//
// Without this call every memory verb would dial a socket whose far end
// has never heard of memory.*.
func registerMemoryHandler(
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus,
	admin *memory.AdminHandler,
) {
	base := memoryStoreDir(paths)
	store := memory.NewFileStore(base, clock)
	sink := memoryJobEventSink{bus: bus}
	// Without the forget pipeline (G/S-14.T4) memory.forget would tombstone
	// the file and stop: no index scrub, no note to the backup lane.
	memory.NewHandler(store, clock, memoryForgetOption(base, store, clock, sink)).Register(registry)
	memory.NewSoulHandler(memory.NewFileSoulStore(base, clock, soulDivergenceSink{bus})).Register(registry)
	// The memory.review.* namespace (G/S-14.T3). Its queue is built over
	// the SAME store tree and a candidate ledger rooted at the same base
	// the scheduled digest job uses. Two ledger values over one tree is
	// not two mechanisms: the ledger holds no in-process state, every
	// write it makes is one atomic file write, and the files ARE the
	// state — unlike the Consolidator, whose in-process re-entrancy guard
	// is exactly why the manual and scheduled consolidation paths must
	// share one value.
	ledger := memory.NewFileCandidateLedger(base, store, clock, sink)
	review.NewHandler(review.NewQueue(ledger, clock, sink)).Register(registry)
	// admin is nil only where no scheduler was wired (the Windows daemon
	// stub, and any future caller that serves RPC without background
	// jobs). Registering memory.consolidate against a nil handler would
	// turn the verb into a panic at the far end of an RPC, so the verb is
	// simply absent there and the CLI reports an unknown method.
	if admin != nil {
		admin.Register(registry)
	}
}

// soulDivergenceSink publishes the SOUL store's conflict event on the real
// bus. It is the composition root's adapter for the same reason
// runtimeEventBusAdapter is: internal/memory declares the sink it
// needs structurally and never imports internal/events, so the memory
// store stays testable with no bus at all.
//
// The published payload is the event's own fields — versions, digests, an
// instant — and carries no soul text. That is not incidental: a bus event
// fans out to every subscriber, and the SOUL is the one document in the
// system that must not travel to a subscriber that only asked to know
// something changed.
type soulDivergenceSink struct {
	bus *events.Bus
}

// SoulDiverged publishes ev in the memory namespace. A nil bus discards
// the event, which is the documented no-bus configuration rather than a
// nil-pointer panic inside a store the daemon otherwise serves fine.
func (s soulDivergenceSink) SoulDiverged(ctx context.Context, ev memory.DivergenceEvent) error {
	if s.bus == nil {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.bus.Publish(ctx, soulEventNamespace, events.EventKind(ev.EventName()),
		soulEventSource, payload)
	return err
}

// The bus coordinates the SOUL's divergence event is published under.
const (
	soulEventNamespace = "memory"
	soulEventSource    = "internal/memory"
)
