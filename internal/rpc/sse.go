package rpc

// Purpose (this file): GET /events, the SSE bridge from the daemon's real
//   internal/events.Bus to a subscribed HTTP client (D/S-06.T4): filter
//   parsing/validation, resume-token decoding, and the connection loop
//   (prelude, event forwarding, injected-clock heartbeat, clean
//   unsubscribe on disconnect).
// Inputs: an HTTP GET request's "filter" query parameter and Last-Event-ID
//   header, plus events from a real Subscribe on the constructed bus.
// Outputs: an SSE stream (id/data lines, periodic ": keep-alive"
//   comments), or an HTTP 400 (before the SSE handshake) for an
//   unrecognized filter type.
// Constraints: parseResumeToken is a fuzz target (fuzz_sse_test.go) and
//   never panics or errors, per Last-Event-ID's SHOULD semantics
//   (R-14.13). The heartbeat reads only the injected runtime.Clock.
//
// Known gap: the contract names "the registered event-type registry from
// C-S04.T3" and one unified stream; neither exists in the code this
// extends. internal/events.EventKind is deliberately open (types.go: any
// producer mints its own kind, no registry) and Bus.Subscribe fans in
// exactly one namespace, never across namespaces (every existing
// producer already owns its own namespace). This handler binds to ONE
// namespace and takes an externally-supplied KnownEventKind predicate
// instead of consulting a registry that does not exist; namespace and
// predicate choice belong to the composition root (out of files_scope).
// Full writeup in this ticket's completion report.
//
// SPORT: internal.rpc.SSEHandler/ADDED (P1-E04-W1-S06-T4).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// EventsPath is the single route SSEHandler serves, mirroring RPCPath's
// role for Handler.
const EventsPath = "/events"

// sseHeartbeatInterval is R-14.13's ratified silence window: after this
// long with no event forwarded, a comment line keeps the connection alive
// through idle-timing proxies.
const sseHeartbeatInterval = 15 * time.Second

// ssePollInterval is the real-time cadence the loop wakes on to re-check
// the injected clock against sseHeartbeatInterval — a scheduling cadence,
// never the elapsed-time source itself (clock.Now() always is): Clock has
// no timer/ticker primitive, only Now().
const ssePollInterval = 200 * time.Millisecond

// sseSubscribeBuffer bounds how far a single SSE connection's delivery
// goroutine may run ahead of what has actually been flushed to the client.
const sseSubscribeBuffer = 64

// KnownEventKind reports whether kind is subscribable for this handler's
// namespace. See the "Known gap" note above: no registry exists to
// consult, so the composition root supplies this predicate directly.
type KnownEventKind func(events.EventKind) bool

// SSEHandler is the GET /events http.Handler.
type SSEHandler struct {
	bus       *events.Bus
	namespace string
	known     KnownEventKind
	clock     runtime.Clock
}

// NewSSEHandler builds an SSEHandler bridging bus's namespace namespace to
// SSE clients. known validates each requested filter type; clock drives
// the heartbeat.
func NewSSEHandler(bus *events.Bus, namespace string, known KnownEventKind, clock runtime.Clock) *SSEHandler {
	return &SSEHandler{bus: bus, namespace: namespace, known: known, clock: clock}
}

// ServeHTTP implements http.Handler.
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if goruntime.GOOS == "windows" {
		http.Error(w, "GET /events: the daemon IPC unix socket is not supported on Windows (tier-2); run this from a POSIX daemon (macOS or Linux) instead", http.StatusNotImplemented)
		return
	}
	if r.URL.Path != EventsPath || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	kinds, badType, ok := parseFilter(r.URL.Query().Get("filter"), h.known)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown event type in filter: %q", badType), http.StatusBadRequest)
		return
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sub, startAt, err := h.openSubscription(r.Context(), r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, "failed to subscribe: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeSSEPrelude(w)
	flusher.Flush()
	h.stream(r.Context(), w, flusher, sub, kinds, startAt)
}

// openSubscription resolves Last-Event-ID, subscribes under a fresh
// per-connection cursor name (never reused, so concurrent clients never
// conflict), and computes the Seq every forwarded event must exceed: the
// decoded resume cursor, or the namespace's current tail if absent/invalid.
func (h *SSEHandler) openSubscription(ctx context.Context, lastEventID string) (*events.Subscription, uint64, error) {
	startAt, resumed := parseResumeToken(lastEventID)
	if !resumed {
		tail, err := currentTailSeq(ctx, h.bus, h.namespace)
		if err != nil {
			return nil, 0, err
		}
		startAt = tail
	}
	sub, err := h.bus.Subscribe(ctx, h.namespace, newConnCursorName(), sseSubscribeBuffer)
	if err != nil {
		return nil, 0, err
	}
	return sub, startAt, nil
}

// currentTailSeq snapshots the namespace's highest Seq at connect time,
// so a no-resume-token client tails from "now", not the whole history.
func currentTailSeq(ctx context.Context, bus *events.Bus, namespace string) (uint64, error) {
	history, err := bus.Replay(ctx, namespace, 0)
	if err != nil {
		return 0, err
	}
	var tail uint64
	for _, ev := range history {
		if ev.Seq > tail {
			tail = ev.Seq
		}
	}
	return tail, nil
}

// newConnCursorName mints a per-connection cursor name so concurrent SSE
// clients never collide on Subscribe's one-active-subscriber-per-name rule.
func newConnCursorName() string {
	var b [16]byte
	// nolint:forbidigo // crypto/rand.Read, not math/rand.Read — shared
	// selector text "rand.Read" (elevation_ledger.go: same limitation).
	_, _ = rand.Read(b[:])
	return "sse:" + hex.EncodeToString(b[:])
}

// writeSSEPrelude writes the required SSE headers.
func writeSSEPrelude(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

// stream runs the connection loop until ctx is canceled or the
// subscription reports a fatal error: forward matching, not-yet-seen
// events, heartbeat after sseHeartbeatInterval of injected-clock silence.
// Bus.Unsubscribe blocks until its delivery goroutine has fully stopped,
// so once stream (and so ServeHTTP) returns, no goroutine remains.
func (h *SSEHandler) stream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sub *events.Subscription, kinds filterSet, startAt uint64) {
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
			if ev.Seq <= startAt || !kinds.matches(ev.Kind) {
				continue
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

// writeSSEEvent writes one event as an SSE "id"/"data" record. id is the
// opaque resume token for this event's own Seq, so reconnecting with it as
// Last-Event-ID resumes immediately after it.
func writeSSEEvent(w http.ResponseWriter, ev events.Event) {
	_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", formatResumeToken(ev.Seq), sseEventJSON(ev))
}

// sseEventJSON renders ev's data line: Payload is opaque bytes (see
// events/types.go), so this wraps it in a minimal JSON envelope a
// consumer can recover Kind/Source/Payload from.
func sseEventJSON(ev events.Event) string {
	return fmt.Sprintf(`{"seq":%d,"kind":%q,"source":%q,"payload":%q}`,
		ev.Seq, string(ev.Kind), ev.Source, base64.StdEncoding.EncodeToString(ev.Payload))
}

// filterSet is the parsed, validated form of the "filter" query parameter.
// A nil/empty filterSet matches every kind (subscribe-all).
type filterSet map[events.EventKind]struct{}

func (f filterSet) matches(kind events.EventKind) bool {
	if len(f) == 0 {
		return true
	}
	_, ok := f[kind]
	return ok
}

// parseFilter splits raw on commas, trims whitespace, and validates each
// non-empty type against known. An empty raw string means subscribe-all.
// The first unrecognized type is returned with ok=false so the caller can
// reject the request before the SSE handshake, naming the offender.
func parseFilter(raw string, known KnownEventKind) (kinds filterSet, badType string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", true
	}
	kinds = make(filterSet)
	for _, part := range strings.Split(raw, ",") {
		kind := events.EventKind(strings.TrimSpace(part))
		if kind == "" {
			continue
		}
		if known == nil || !known(kind) {
			return nil, string(kind), false
		}
		kinds[kind] = struct{}{}
	}
	return kinds, "", true
}

// resumeTokenSize matches internal/events/cursor.go's cursorValueSize: an
// 8-byte big-endian Seq. R-14.13 names the Last-Event-ID value as an
// opaque base64url WRAPPING of that exact representation — this constant
// keeps the two in lockstep by construction rather than by convention.
const resumeTokenSize = 8

// formatResumeToken encodes seq as R-14.13's opaque base64url resume
// token.
func formatResumeToken(seq uint64) string {
	var buf [resumeTokenSize]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

// parseResumeToken decodes an opaque Last-Event-ID value. It NEVER errors:
// absent/malformed/unrecognized decodes as (0, false) — "open at tail"
// (R-14.13's SHOULD semantics). Only the CANONICAL base64url encoding of
// exactly resumeTokenSize bytes counts as a real cursor — base64 is not
// injective over non-canonical input, so this re-encodes and requires an
// exact match before trusting the decode. FuzzParseResumeToken proves
// this holds for arbitrary input.
func parseResumeToken(raw string) (seq uint64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, false
	}
	if len(decoded) != resumeTokenSize {
		return 0, false
	}
	seq = binary.BigEndian.Uint64(decoded)
	if formatResumeToken(seq) != raw {
		return 0, false
	}
	return seq, true
}
