package rpc

// Purpose (this file): GET EventsPath's topic-dispatching front door
//   (P1-E12-W6-S121-T1): a request with no key naming a topic (by any
//   casing or padding) serves Default, the existing daemon-wide
//   SSEHandler — an unrelated query key (Default's own "filter", say) is
//   none of this mux's business; a request carrying exactly one topic
//   value, under the byte-exact key "topic", that matches a registered
//   key serves that handler; every other shape (an unknown, empty,
//   case-variant, padded or repeated topic key) is refused before either
//   handler's own subscribe-time logic ever runs. It also owns the two
//   request-context helpers that carry an event-visibility predicate from
//   whoever installs one down to the topic handler.
// Inputs: a GET request's query string, inspected for any key naming a
//   topic parameter; optionally an event-visibility predicate already
//   installed on the request context.
// Outputs: delegates ServeHTTP to Default or the matching Topics entry;
//   or an HTTP 400 with a JSON-RPC-shaped KindInvalidInput body whose
//   message is always one of a small set of fixed strings — it never
//   echoes the query string or any value from it, so a hostile topic
//   value (an escape sequence, an oversized string) never reaches a
//   client reading the refusal.
// Constraints: FAIL-CLOSED: an unrecognized or ambiguous topic is
//   refused, never silently served the default/daemon stream. ServeHTTP's
//   first action is guardLocalRequest(w, r) (request_guard.go, R-14.312),
//   reused rather than re-derived, so every topic path gets the same
//   owner-peer and browser-shape refusal rpc.Handler's own GET /events
//   route enforces, even if a future composition root mounts this mux
//   somewhere Handler.ServeHTTP's guard never runs. No topic parsing, no
//   Topics dispatch and no delegate Subscribe happens before that guard
//   passes. Each delegate handler still performs its own subscribe-time
//   checks on top of this: the guard is a floor, not a replacement.
//
// SPORT: internal.rpc.SSEMux/ADDED (P1-E12-W6-S121-T1).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// topicParam is the query parameter SSEMux dispatches on.
const topicParam = "topic"

// maxTopicLen bounds a topic value before it is used as a map key or an
// error is built around it: no registered topic is remotely this long
// ("fleet.sessions" is 15 bytes), so a longer value is refused before the
// map lookup rather than accepted and hashed.
const maxTopicLen = 256

// SSEMux is the GET EventsPath http.Handler that dispatches by the
// "topic" query parameter. The zero value has a nil Default and an empty
// Topics map, so every request refuses until both are set.
type SSEMux struct {
	// Default handles a request that carries no topic parameter at all:
	// the existing daemon-wide SSEHandler.
	Default http.Handler
	// Topics maps a non-empty topic value to the handler that serves it.
	Topics map[string]http.Handler
}

// NewSSEMux builds an SSEMux with defaultHandler and an empty Topics
// table, ready for WithTopic.
func NewSSEMux(defaultHandler http.Handler) *SSEMux {
	return &SSEMux{Default: defaultHandler, Topics: make(map[string]http.Handler)}
}

// WithTopic registers handler to serve topic and returns m, so calls
// chain: NewSSEMux(def).WithTopic("a", h1).WithTopic("b", h2).
func (m *SSEMux) WithTopic(topic string, handler http.Handler) *SSEMux {
	m.Topics[topic] = handler
	return m
}

// ServeHTTP implements http.Handler. Order: guardLocalRequest, then the
// topic parameter, then dispatch. A request with no key that names a
// topic at all (by any casing or padding) serves Default — an unrelated
// query key a topic handler or the default stream defines for itself
// (Default's own "filter", say) is none of this mux's business. The
// parameter must be present exactly once, under the byte-exact key
// "topic", with a registered value; anything that only LOOKS like a
// topic key — a case variant ("Topic"), a padded one (a leading space
// from "+topic"/"%20topic"), a repeat of the exact key, or a second key
// that folds to "topic" alongside the real one — is refused rather than
// resolved one way or another, so two parsers (this mux, a proxy, a
// browser) can never disagree about which stream a request named. The
// raw query is parsed strictly: r.URL.Query() silently drops a malformed
// pair (a bad %-escape, a semicolon), which could hide a topic parameter
// and fall through to Default, so a malformed query is refused instead.
func (m *SSEMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !guardLocalRequest(w, r) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeTopicRefusal(w, "malformed /events query string")
		return
	}
	switch topicLikeKeyCount(query) {
	case 0:
		m.serveDefault(w, r)
		return
	case 1:
		// Fall through only when that one key is byte-exact "topic";
		// a lone case/whitespace variant is refused below.
	default:
		writeTopicRefusal(w, "exactly one /events topic parameter is allowed")
		return
	}
	values, exact := query[topicParam]
	if !exact || len(values) != 1 {
		writeTopicRefusal(w, "exactly one /events topic parameter is allowed")
		return
	}
	topic := values[0]
	if len(topic) > maxTopicLen {
		writeTopicRefusal(w, "unknown /events topic")
		return
	}
	if handler, ok := m.Topics[topic]; ok && topic != "" {
		handler.ServeHTTP(w, topicRequest(r))
		return
	}
	writeTopicRefusal(w, "unknown /events topic")
}

// topicLikeKeyCount counts query keys that name a topic parameter under
// any casing or padding: EqualFold on the trimmed key against "topic".
// It is a count, not the matched keys, because ServeHTTP only needs to
// know whether there is zero, exactly one (worth inspecting further) or
// more than one (always a refusal, never a guess at which one was meant).
func topicLikeKeyCount(query url.Values) int {
	n := 0
	for key := range query {
		if strings.EqualFold(strings.TrimSpace(key), topicParam) {
			n++
		}
	}
	return n
}

// serveDefault dispatches to m.Default, or refuses (404, never the
// topic 400: there was no topic to be wrong about) when no Default is
// configured.
func (m *SSEMux) serveDefault(w http.ResponseWriter, r *http.Request) {
	if m.Default == nil {
		http.NotFound(w, r)
		return
	}
	m.Default.ServeHTTP(w, r)
}

// eventVisibleKey is the context key WithEventVisible stores its
// predicate under.
type eventVisibleKey struct{}

// denyAllEvents is the predicate WithEventVisible installs for a nil
// argument: unknown is never permission.
func denyAllEvents(string, string) bool { return false }

// WithEventVisible returns a copy of ctx carrying pred, the per-event
// visibility predicate a topic stream consults for each event's session
// id and parent session id. A nil pred installs a predicate that hides
// every event (fail-closed), never "no filter". A later call on a derived
// context replaces the earlier predicate, as context values do.
func WithEventVisible(ctx context.Context, pred func(sessionID, parentSessionID string) bool) context.Context {
	if pred == nil {
		pred = denyAllEvents
	}
	return context.WithValue(ctx, eventVisibleKey{}, pred)
}

// EventVisibleFrom returns the predicate WithEventVisible installed on
// ctx. ok is false when none was installed.
func EventVisibleFrom(ctx context.Context) (func(sessionID, parentSessionID string) bool, bool) {
	pred, ok := ctx.Value(eventVisibleKey{}).(func(sessionID, parentSessionID string) bool)
	return pred, ok && pred != nil
}

// topicRequest is the one place SSEMux hands request-scoped state to a
// topic handler: when an event-visibility predicate is installed, the
// handler's request carries that same predicate, re-pinned on the context
// it is served with, so the handoff is an explicit line of this mux
// rather than an accident of context inheritance.
func topicRequest(r *http.Request) *http.Request {
	pred, ok := EventVisibleFrom(r.Context())
	if !ok {
		return r
	}
	return r.WithContext(WithEventVisible(r.Context(), pred))
}

// writeTopicRefusal writes the JSON-RPC-shaped KindInvalidInput refusal
// for a topic parameter SSEMux will not dispatch: HTTP 400, before any SSE
// handshake on any delegate handler. The body shape mirrors registry.go's
// own error wire shape (ResponseEnvelope wrapping an ErrorObject), so a
// client already parsing POST RPCPath's JSON-RPC error envelopes can parse
// this refusal the same way.
func writeTopicRefusal(w http.ResponseWriter, message string) {
	errObj := &ErrorObject{
		Code:    cascade.KindInvalidInput.JSONRPCCode(),
		Message: message,
		Data:    map[string]string{"kind": cascade.KindInvalidInput.String()},
	}
	env := NewEnvelope(nil, nil, errObj)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(env)
}
