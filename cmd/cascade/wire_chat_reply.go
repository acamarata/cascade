//go:build !windows

// Purpose: registers the chat-reply-topic daemon wiring: the owner-view SSE
// topic "chat.reply" on GET /events, served over the daemon's one process
// ReplyHub (P1-CPA-13c).
//
// WHO MAY READ A STREAM. A reply stream carries a model's reply text, so a
// stream is served only when BOTH checks admit the caller:
//
//   - the stream binding: the hub's Subscribe refuses a stream id that
//     chat.reply_open bound to another caller identity (KindPermissionDenied,
//     HTTP 403, before any SSE handshake and with zero frames written);
//   - the event-visibility predicate: a principal caller is served only when
//     the predicate rpc.Handler installed (rpc.WithEventVisible) admits the
//     principal's own session; it is consulted again before every frame. An
//     owner caller carries no predicate and is admitted by its Caller alone;
//     a predicate on an owner request is honoured, never skipped.
//
// The binding never stands in for the predicate, nor the predicate for the
// binding: each refuses on its own.
//
// Inputs: GET /events?topic=chat.reply&stream_id=<id> (the shipped cascade-pa
// client's key) or &stream=<id>; exactly one stream id parameter in all.
// Outputs: one SSE "data:" record per conversation.Frame, ending after the
// terminal done or error frame; or a JSON-RPC-shaped refusal before the
// handshake (400 malformed, 403 refused, 404 unknown or expired).
// Constraints: the hub is per daemon composition (one per registry), shared
// by the topic and the Replier the composition root builds through
// wireChatReply; reply content is never logged.
//
// SPORT: cmd/cascade daemon registrations (P1-CORE-01) — chat.reply topic,
// P1-CPA-13c.
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// chatReplyTopicName is the SSE topic a reply stream is served on.
const chatReplyTopicName = "chat.reply"

// maxReplyStreamIDLen bounds a stream id value: a minted id is 32 hex bytes.
const maxReplyStreamIDLen = 64

var _ = rpc.DeclareTopicAccess("chat.reply", rpc.AccessOwnerView)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "chat-reply-topic", Phase: phaseEventTopics, Order: 40,
	Wire: func(w *daemonWiring) error {
		hub, err := chatReplyHub(w)
		if err != nil {
			return err
		}
		w.Events.WithTopic(chatReplyTopicName, &chatReplyTopic{hub: hub})
		return nil
	},
})

var (
	errReplyTopicNoCaller = cascade.New(cascade.KindPermissionDenied, "chat.reply: stream request carries no caller")
	errReplyTopicHidden   = cascade.New(cascade.KindPermissionDenied, "chat.reply: stream not visible to this caller")
	errReplyTopicStreamID = cascade.New(cascade.KindInvalidInput, "chat.reply: exactly one stream id parameter is required")
	errReplyTopicNoFlush  = cascade.New(cascade.KindInternal, "chat.reply: response writer cannot stream")
)

// replyHubs holds one ReplyHub per daemon composition, keyed by its registry.
var replyHubs = struct {
	mu   sync.Mutex
	hubs map[*rpc.Registry]*conversation.ReplyHub
}{hubs: map[*rpc.Registry]*conversation.ReplyHub{}}

// chatReplyHub returns the one ReplyHub of the daemon composed over
// w.Registry, building it on first use. The topic and the Replier both take
// it from here, so a stream chat.reply_open mints is the stream this topic
// serves.
func chatReplyHub(w *daemonWiring) (*conversation.ReplyHub, error) {
	replyHubs.mu.Lock()
	defer replyHubs.mu.Unlock()
	if hub, ok := replyHubs.hubs[w.Registry]; ok {
		return hub, nil
	}
	hub, err := conversation.NewReplyHub(w.Clock)
	if err != nil {
		return nil, err
	}
	replyHubs.hubs[w.Registry] = hub
	return hub, nil
}

// chatReplyTopic serves one reply stream to the caller it is bound to.
type chatReplyTopic struct{ hub *conversation.ReplyHub }

// ConsumesEventVisible marks the topic as applying the event predicate to
// every frame (rpc.EventVisibleConsumer).
func (*chatReplyTopic) ConsumesEventVisible() {}

// ServeHTTP checks the caller, the predicate and the stream binding, in
// that order, before the SSE handshake, then forwards frames to the
// terminal one.
func (t *chatReplyTopic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller, ok := principal.CallerFrom(r.Context())
	if !ok {
		writeReplyTopicRefusal(w, errReplyTopicNoCaller)
		return
	}
	visible := replyStreamVisible(r, caller)
	if !visible() {
		writeReplyTopicRefusal(w, errReplyTopicHidden)
		return
	}
	id, err := replyStreamID(r.URL.RawQuery)
	if err != nil {
		writeReplyTopicRefusal(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeReplyTopicRefusal(w, errReplyTopicNoFlush)
		return
	}
	frames, err := t.hub.Subscribe(r.Context(), id, caller)
	if err != nil {
		writeReplyTopicRefusal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for f := range frames {
		if !visible() {
			return
		}
		data, err := json.Marshal(f)
		if err != nil {
			return
		}
		if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
			return
		}
		flusher.Flush()
		if f.Type != conversation.FrameDelta {
			return
		}
	}
}

// replyStreamVisible returns the per-frame visibility check for caller: the
// installed predicate over the principal's own session when one is present,
// else admitted only for an owner caller (a principal with no predicate is
// never admitted: unknown is never permission).
func replyStreamVisible(r *http.Request, caller principal.Caller) func() bool {
	pred, has := rpc.EventVisibleFrom(r.Context())
	var sessionID, parentID string
	if caller.Principal != nil {
		sessionID, parentID = caller.Principal.SessionID, caller.Principal.ParentSessionID
	}
	return func() bool {
		if has {
			return pred(sessionID, parentID)
		}
		return caller.Principal == nil
	}
}

// replyStreamID reads the one stream id from rawQuery: exactly one value
// across the "stream_id" and "stream" keys, non-empty and bounded.
func replyStreamID(rawQuery string) (string, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", errReplyTopicStreamID
	}
	values := append(append([]string(nil), q["stream_id"]...), q["stream"]...)
	if len(values) != 1 || values[0] == "" || len(values[0]) > maxReplyStreamIDLen {
		return "", errReplyTopicStreamID
	}
	return values[0], nil
}

// writeReplyTopicRefusal writes err as a JSON-RPC-shaped envelope before any
// SSE handshake: the status follows the error's kind, the body carries the
// kind and the error's own message, never a query value.
func writeReplyTopicRefusal(w http.ResponseWriter, err error) {
	kind, known := cascade.KindOf(err)
	if !known {
		kind = cascade.KindInternal
	}
	msg := kind.String()
	if e, ok := err.(*cascade.Error); ok && e.Msg != "" { //nolint:errorlint // the sentinel itself, not a wrapped chain
		msg = e.Msg
	}
	status := http.StatusServiceUnavailable
	switch kind {
	case cascade.KindPermissionDenied:
		status = http.StatusForbidden
	case cascade.KindNotFound:
		status = http.StatusNotFound
	case cascade.KindInvalidInput:
		status = http.StatusBadRequest
	case cascade.KindInternal:
		status = http.StatusInternalServerError
	case cascade.KindConflict, cascade.KindUnavailable, cascade.KindTimeout, cascade.KindCanceled,
		cascade.KindElevationRequired, cascade.KindPolicyDenied, cascade.KindCapabilityDenied,
		cascade.KindQuotaExhausted, cascade.KindUnsupported, cascade.KindIntegrity:
		status = http.StatusServiceUnavailable
	}
	errObj := &rpc.ErrorObject{Code: kind.JSONRPCCode(), Message: msg,
		Data: map[string]string{"kind": kind.String()}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpc.NewEnvelope(nil, nil, errObj))
}
