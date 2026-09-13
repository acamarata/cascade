// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's fix for
// `cascade memory remember/recall/forget/list`: the daemonless probe
// printed "running in embedded (daemonless) mode" and these four verbs
// then built a client and dialed the daemon anyway (memory_call.go's
// memoryCallRaw never consulted runtime.DaemonlessStateFrom), so every
// fresh install with no daemon running failed to record anything instead
// of writing the record it promised to. This file adds the same branch
// every other daemonless command already carries (context_cmd.go's
// fetchContextSlice, recall_embedded.go's recallQuery): consult the probe
// once, dial the daemon only when it confirmed one is live, and otherwise
// answer from the identical memory.Handler composition
// registerMemoryHandler builds for the daemon (daemon_unix_run_memory.go),
// invoked through the SAME bound method value the daemon's RPC dispatcher
// calls, so the embedded and daemon paths can never disagree about what a
// memory verb means.
//
// `memory soul`, `memory consolidate` and `memory review` are NOT covered
// here: DEFECT-cli-surfaces-promise-embedded-mode.md's own audit named
// only memory.go's four data verbs as broken, and those three surfaces
// carry daemon-resident state (a cron-scheduled consolidator, a review
// ledger) this ticket never touched. They still route through the
// unmodified memoryCall/memoryCallRaw path, so they still dial
// unconditionally today — a real, narrower instance of the same DEFECT
// class, filed as a follow-up rather than swept into this fix.
//
// Inputs: the memory.go RunE closures' already-built params.
// Outputs: the same result value memoryCall would have decoded into out,
// or a scrubbed taxonomy error.
//
// Constraints: no index scrub (forget.Pipeline.WithIndex) is wired here,
// matching registerMemoryHandler's own comment: no projection job runs in
// a one-shot CLI invocation, so memory.forget's result honestly reports
// that no index was scrubbed rather than claiming a scrub it never ran.
// The forget pipeline's event sink is nil (which forget.NewPipeline's own
// doc comment names as "the documented no-bus configuration"), for the
// identical reason resolveRecallEmbedded's vector leg takes a nil event
// sink: a one-shot CLI invocation has no daemon-owned event bus to
// publish a retirement notice onto.
//
// SPORT: cmd.cascade.cmd.memory (FIX, embedded-path routing).
package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/forget"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memoryRoute is memory.go's four data verbs' entry point: it routes
// through the client when the daemonless probe confirms a live daemon,
// mirroring memoryCall exactly, and through the embedded memory.Handler
// otherwise. An undecidable probe result (ok=false) is treated as
// embedded, matching recallQuery's identical rule (recall_embedded.go).
func memoryRoute(cmd *cobra.Command, deps memoryDeps, method string, params, out any) error {
	if st, ok := runtime.DaemonlessStateFrom(cmd.Context()); ok && !st.Embedded {
		return memoryCall(cmd, deps, method, params, out)
	}
	return scrubDiagnostic(memoryCallEmbedded(cmd.Context(), deps, method, params, out))
}

// memoryCallEmbedded builds the embedded memory.Handler and dispatches
// method to its matching bound method, round-tripping params/out through
// JSON exactly as a real RPC call would, so this file adds no second,
// independently-maintained decode/encode step.
func memoryCallEmbedded(ctx context.Context, deps memoryDeps, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade memory: encode params")
	}
	fn, ok := memoryEmbeddedMethods(newEmbeddedMemoryHandler(deps.Paths))[method]
	if !ok {
		return cascade.Newf(cascade.KindUnsupported, "cascade memory: %s has no embedded implementation", method)
	}
	result, err := fn(ctx, raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade memory: encode result")
	}
	return json.Unmarshal(encoded, out)
}

// newEmbeddedMemoryHandler builds the same store/pipeline composition
// registerMemoryHandler builds for the daemon (daemon_unix_run_memory.go):
// the same memoryStoreDir root, the same memory.NewFileStore, and the same
// forget.NewPipeline shape — minus the index scrub and event bus neither
// exists in a one-shot CLI process (see this file's header comment).
func newEmbeddedMemoryHandler(paths runtime.PathProvider) *memory.Handler {
	base := memoryStoreDir(paths)
	clock := runtime.NewSystemClock()
	store := memory.NewFileStore(base, clock)
	pipeline := forget.NewPipeline(base, store, clock, nil)
	return memory.NewHandler(store, clock, memory.WithForgetPipeline(pipeline))
}

// memoryEmbeddedMethods maps each memory.* method name to h's matching
// bound method value, mirroring Handler.Register's own map exactly
// (internal/memory/rpc.go) so a drifting method set is caught by a
// missing map entry rather than answered wrong.
func memoryEmbeddedMethods(h *memory.Handler) map[string]func(context.Context, json.RawMessage) (any, error) {
	return map[string]func(context.Context, json.RawMessage) (any, error){
		memory.MethodRemember: h.Remember,
		memory.MethodRecall:   h.Recall,
		memory.MethodForget:   h.Forget,
		memory.MethodList:     h.List,
	}
}
