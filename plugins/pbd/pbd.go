// Package pbd is the first-party builtin cascade-pbd plugin: it registers
// the ratified `pbd validate`, `pbd lint`, `pbd create`, `pbd edit`, and
// `pbd move` commands (07-CLI-COMMAND-TREE.md's plugin-contributed pbd
// namespace) with the host's compile-time builtin registry
// (pkg/plugin.RegisterBuiltin), running the native tree-store/validator/
// lint/authoring engine in internal/pews over the canonical PEWS ticket
// tree.
//
// Purpose: mount validate (T2), lint (T4), and create/edit/move (T3) —
//
//	and ONLY those five — through the builtin-plugin boundary supplied by
//	C/S-05.T7. Later PBD tickets own status/board/lifecycle/dispatch as
//	further commands on this same manifest.
//
// Inputs: RunCommand's args, decoded per-verb — see pbd.go's RunCommand,
//
//	validate.go, lint.go, and author.go for each verb's own arg shape.
//
// Outputs: nil on a clean tree/lint/authoring write; a *cascade.Error of
//
//	kind KindInvalidInput (fail closed) summarizing every violation/issue
//	otherwise — see validate.go, lint.go, and author.go.
//
// Constraints: imports pkg/** and this plugin's own internal/pews ONLY,
//
//	never the repo's internal/** (Art.10.2, internal/build/arch_test.go's
//	plugins-providers-boundary rule) — this is also why RunCommand cannot
//	format output through internal/output.Writer and instead folds every
//	violation/issue into the returned error's message.
//
// SPORT: plugins/pbd (ADD) — P1-E14-W3-S28-T2; lint (ADD) — P1-E14-W3-S28-T4;
// authoring (ADD) — P1-E14-W3-S28-T3; events bridge (ADD) — P1-E14-W3-S29-T1.
package pbd

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// pluginID is this plugin's manifest id.
const pluginID = "pbd"

// validateCommandName and lintCommandName are the CommandSpecs T2 and T4
// mount respectively. createCommandName, editCommandName, and
// moveCommandName (author.go) are T3's.
const validateCommandName = "validate"
const lintCommandName = "lint"

// init registers cascade-pbd with the host's compile-time registry. A
// blank-import of this package by the binary's composition root (or a
// test) is sufficient to make it known to plugin.Builtins().
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns cascade-pbd's cascade.plugin/v2 manifest.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "PBD Engine",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: manifestCommands(),
			Tools:    manifestTools(),
		},
	}
}

// manifestCommands is manifest()'s Provides.Commands, split out purely for
// that function's own line budget (Art.10.5).
func manifestCommands() []plugin.CommandSpec {
	return []plugin.CommandSpec{
		{Name: validateCommandName, Description: "Validate the PEWS ticket tree for structural correctness."},
		{Name: lintCommandName, Description: "Lint every PEWS ticket contract for completeness against the Forge spec."},
		{Name: createCommandName, Description: "Author a new PEWS ticket at the canonical tree position its id implies."},
		{Name: editCommandName, Description: "Overwrite an existing PEWS ticket's contract in place."},
		{Name: moveCommandName, Description: "Relocate a PEWS ticket to a new canonical tree position."},
		{Name: statusCommandName, Description: "Show a summary of the PEWS ticket tree for one phase.", RPCMethod: statusRPCMethod},
		{Name: boardCommandName, Description: "Show the PEWS ticket tree for one phase grouped by model class.", RPCMethod: boardRPCMethod},
	}
}

// manifestTools is manifest()'s Provides.Tools.
func manifestTools() []plugin.ToolSpec {
	return []plugin.ToolSpec{
		{Name: statusToolName, Description: "Read-only PEWS status summary for one phase."},
		{Name: boardToolName, Description: "Read-only PEWS board (grouped by model class) for one phase."},
	}
}

// handlers is cascade-pbd's plugin.BuiltinHandlers implementation.
type handlers struct{}

// DispatchTool is defined in status.go (same package): it services the
// two policy-filtered MCP tools N/S-29.T4 adds (statusToolName,
// boardToolName) and refuses every other name. Kept out of this file
// purely for line-budget room (Art.10.5's 300-line cap).

// DispatchIntent always refuses: this ticket provides no intents.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "pbd: no intent named %q", name)
}

// RunCommand services the five mounted CommandSpecs. validate and lint
// take args[0] (tree root, required) and an optional args[1] phase
// override; create, edit, and move have their own arg shapes documented
// on runCreateCommand/runEditCommand/runMoveCommand (author.go). It
// returns nil on success and a fail-closed *cascade.Error otherwise.
func (handlers) RunCommand(_ context.Context, name string, args []string) error {
	switch name {
	case validateCommandName, lintCommandName:
		if len(args) == 0 || args[0] == "" {
			return cascade.Newf(cascade.KindInvalidInput, "pbd %s: a tree root argument is required", name)
		}
		phase := DefaultPhase
		if len(args) > 1 && args[1] != "" {
			phase = args[1]
		}
		if name == lintCommandName {
			return runLintAndSummarize(args[0], phase)
		}
		return runValidateAndSummarize(args[0], phase)
	case createCommandName:
		return runCreateCommand(args)
	case editCommandName:
		return runEditCommand(args)
	case moveCommandName:
		return runMoveCommand(args)
	case statusCommandName, boardCommandName:
		// runStatusOrBoardCommand is defined in status.go (line-budget).
		return runStatusOrBoardCommand(name, args)
	default:
		return cascade.Newf(cascade.KindUnsupported, "pbd: no command named %q", name)
	}
}

// eventsPath is the route the SSE bridge below expects to be mounted at,
// mirroring internal/rpc.EventsPath's role for the daemon-wide bridge.
const eventsPath = "/events"

// busSubscribeBuffer bounds one connection's delivery backlog, mirroring
// internal/fleet/sessions.sseSubscribeBuffer's sizing.
const busSubscribeBuffer = 64

// busEvent is one projection-update notification carried on bus.
type busEvent struct {
	phase   string
	payload []byte
}

// bus is an in-process, plugin-owned fan-out broadcaster: it implements
// pews.EventPublisher's Publish side and lets an SSE handler subscribe to
// forward events over HTTP/1.1 — without this plugin importing
// internal/events (Art.10.2, plugins-providers-boundary). Publish never
// blocks: a subscriber whose buffer is full drops the update rather than
// stalling the projector that produced it.
type bus struct {
	mu   sync.Mutex
	subs map[int]chan busEvent
	next int
}

func newBus() *bus { return &bus{subs: make(map[int]chan busEvent)} }

// Publish implements pews.EventPublisher.
func (b *bus) Publish(_ context.Context, phase string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- busEvent{phase: phase, payload: payload}:
		default:
		}
	}
	return nil
}

// subscribe registers a new subscriber, returning its id and channel.
func (b *bus) subscribe() (int, chan busEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan busEvent, busSubscribeBuffer)
	b.subs[id] = ch
	return id, ch
}

// unsubscribe removes and closes id's channel. Idempotent: unsubscribing
// an already-removed id is a no-op, never a panic.
func (b *bus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// eventsHandler is the GET /events http.Handler bridging a bus to SSE
// clients, following internal/rpc.SSEHandler's proven connection-loop
// shape (prelude, forward-then-flush, clean unsubscribe on disconnect or
// cancellation) without importing internal/events or internal/runtime.
type eventsHandler struct {
	bus *bus
}

// ServeHTTP implements http.Handler.
func (h *eventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != eventsPath || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	id, ch := h.bus.subscribe()
	defer h.bus.unsubscribe(id)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	streamEvents(r.Context(), w, flusher, ch)
}

// streamEvents forwards ch onto w as SSE records until ctx is canceled or
// ch is closed by an unsubscribe (which never happens concurrently with
// this same connection's own deferred unsubscribe, since each connection
// owns exactly one subscriber id). Returning here is this handler's only
// exit path, so ServeHTTP's deferred unsubscribe always runs and no
// goroutine is ever created by this function — the connection's own
// goroutine (from net/http) is the only one involved.
func streamEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, ch chan busEvent) {
	for {
		select {
		case ev, open := <-ch:
			if !open {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", ev.phase, ev.payload)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// NewEventsHandler builds cascade-pbd's GET /events HTTP handler: the SSE
// bridge from a pews.Projector's projection updates onto the repository's
// existing HTTP/1.1 SSE transport (02-TARGET-STRUCTURE.md's GET /events
// boundary), plus the *pews.Projector already wired to publish onto it.
// The composition root supplies store and clock, mounts the returned
// handler, and calls Project whenever the PEWS tree changes — none of
// which is in this ticket's files_scope (see
// internal/build/testonly-allow.json for the wiring ticket this awaits).
func NewEventsHandler(store provider.Store, clock pews.Clock) (http.Handler, *pews.Projector, error) {
	b := newBus()
	proj, err := pews.NewProjector(pews.ProjectorOptions{Store: store, Clock: clock, Publisher: b})
	if err != nil {
		return nil, nil, err
	}
	return &eventsHandler{bus: b}, proj, nil
}
