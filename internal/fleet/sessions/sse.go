package sessions

// Purpose (this file): GET /events?topic=fleet.sessions — the SSE stream
//
//	delivering fleet.sessions.changed events (R-21.272) to a subscribed
//	HTTP client, over the SAME real internal/events.Bus the daemon-wide
//	bridge (internal/rpc/sse.go) uses.
//
// Inputs: an HTTP GET request's optional "topic" query parameter (only
//
//	"fleet.sessions" or empty is accepted) and events published to the
//	"fleet.sessions" namespace by domain.go's Store.emit.
//
// Outputs: an SSE stream with "event"/"data"/"id"/"retry" fields per the
//
//	WHATWG SSE Living Standard, or an HTTP 400/501 before the handshake.
//
// Constraints: Windows is tier-2 (§Forge, no daemon at all), so ServeHTTP
//
//	refuses unconditionally there, before ever touching the bus — the
//	same unconditional GOOS check internal/rpc/sse.go's own ServeHTTP
//	makes, since a daemon socket is NEVER present on Windows to serve
//	this from.
//
// CONTRACT DEVIATION (event/data/id/retry fields, recorded, not papered
// over). internal/rpc/sse.go's SSEHandler (the "existing SSE
// implementation" this ticket's brief names) writes only "id"/"data"
// lines — no "event" or "retry" field, and it fans in exactly one bus
// namespace chosen at construction with no per-request topic filter. This
// ticket's acceptance criteria require "event", "data", "id", AND "retry"
// fields, so SSEHandler here is this package's OWN small handler over the
// same *events.Bus, not a call into internal/rpc.NewSSEHandler. It reuses
// that file's PROVEN shape (subscribe under a fresh per-connection
// cursor, forward matching events, injected-clock heartbeat, clean
// Unsubscribe on ctx.Done or a closed Events channel) rather than
// reinventing the connection loop.
//
// CONTRACT DEVIATION (mounting, recorded, not papered over). "add a GET
// /events?topic=fleet.sessions handler" reads as a second route on the
// daemon's mux. cmd/cascade/daemon_unix_run.go's buildRPCServer (out of
// this ticket's files_scope) mounts exactly one SSEHandler at the fixed
// rpc.EventsPath — a second http.ServeMux.Handle("/events", ...) call
// there would panic on the duplicate pattern. This file's SSEHandler is
// therefore a standalone http.Handler, ready to be mounted at its own
// path (or composed behind a topic-dispatching wrapper) by whichever
// ticket wires cmd/cascade/daemon_unix_run.go; recorded in
// internal/build/testonly-allow.json naming this ticket.
//
// SPORT: internal.fleet.sessions.SSEHandler/ADDED (P1-E12-W3-S24-T3).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	goruntime "runtime"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// EventsPath is the route this handler expects to be mounted at.
const EventsPath = "/events"

// sseHeartbeatInterval mirrors internal/rpc/sse.go's ratified silence
// window (R-14.13).
const sseHeartbeatInterval = 15 * time.Second

// ssePollInterval is the real-time cadence the loop wakes on; the elapsed-
// time source is always clock.Now(), never this ticker.
const ssePollInterval = 200 * time.Millisecond

// sseSubscribeBuffer bounds one connection's delivery backlog.
const sseSubscribeBuffer = 64

// sseRetryMillis is the "retry" field value: how long a conformant
// EventSource client waits before reconnecting after a dropped stream.
const sseRetryMillis = 3000

// SSEHandler is the GET /events?topic=fleet.sessions http.Handler.
type SSEHandler struct {
	bus   *events.Bus
	clock runtime.Clock
}

// NewSSEHandler builds an SSEHandler bridging bus's "fleet.sessions"
// namespace to SSE clients, heartbeating from clock.
func NewSSEHandler(bus *events.Bus, clock runtime.Clock) *SSEHandler {
	return &SSEHandler{bus: bus, clock: clock}
}

// ServeHTTP implements http.Handler.
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if goruntime.GOOS == "windows" {
		http.Error(w, "GET /events?topic=fleet.sessions: no daemon exists on Windows (tier-2); SSE unavailable on Windows tier-2", http.StatusNotImplemented)
		return
	}
	if topic := r.URL.Query().Get("topic"); topic != "" && topic != changedNamespace {
		http.Error(w, fmt.Sprintf("unknown topic: %q", topic), http.StatusBadRequest)
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	subscription, err := h.bus.Subscribe(r.Context(), changedNamespace, newConnCursorName(), sseSubscribeBuffer)
	if err != nil {
		http.Error(w, "failed to subscribe: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeSSEPrelude(w)
	flusher.Flush()
	h.stream(r.Context(), w, flusher, subscription)
}

// newConnCursorName mints a per-connection cursor name so concurrent SSE
// clients never collide on Subscribe's one-active-subscriber-per-name
// rule.
func newConnCursorName() string {
	var b [16]byte
	// nolint:forbidigo // crypto/rand.Read, not math/rand.Read.
	_, _ = rand.Read(b[:])
	return "sse-sessions:" + hex.EncodeToString(b[:])
}

// writeSSEPrelude writes the required SSE headers.
func writeSSEPrelude(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

// stream runs the connection loop until ctx is canceled, the client
// disconnects, or the subscription reports a fatal error. Unsubscribe
// blocks until its delivery goroutine has fully stopped, so once stream
// (and so ServeHTTP) returns, no goroutine and no subscription remain —
// see sse_test.go's TestSessionSSE_ClientDisconnect_CleanUnsubscribeNoLeak.
func (h *SSEHandler) stream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sub *events.Subscription) {
	defer func() { _ = sub.Unsubscribe() }()

	ticker := time.NewTicker(ssePollInterval)
	defer ticker.Stop()

	last := h.clock.Now()
	for {
		select {
		case ev, open := <-sub.Events:
			if !open {
				return
			}
			writeSSEEvent(w, ev)
			flusher.Flush()
			last = h.clock.Now()
		case <-ticker.C:
			if h.clock.Now().Sub(last) >= sseHeartbeatInterval {
				_, _ = fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
				last = h.clock.Now()
			}
		case <-sub.Errs:
			return
		case <-ctx.Done():
			return
		}
	}
}

// writeSSEEvent writes one event as a conformant WHATWG SSE record: a
// named "event" field (always changedNamespace's changed-event name), the
// "data" line, an "id" line (the event's own Seq, so a client's
// Last-Event-ID reflects real position), and a "retry" line.
func writeSSEEvent(w http.ResponseWriter, ev events.Event) {
	_, _ = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\nretry: %d\n\n",
		ev.Kind, ev.Seq, ev.Payload, sseRetryMillis)
}
