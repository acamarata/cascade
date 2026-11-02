package conversation

// Purpose: the chat.reply_open and chat.reply verbs, the request checks
//   both run before any dispatch, and the reply's stream and persist steps.
// Inputs: {"thread_id","turn_id"} for chat.reply_open; {"stream_id"} for
//   chat.reply. Unknown fields are refused.
// Outputs: {"stream_id"}; {"thread_id","turn_id","assistant_turn_id","replayed"}.
// Constraints: both verbs are AccessOwnerView: the owner replies on any
//   thread, a principal only on a thread attributed to its own scope.
//   Generation runs in the request goroutine, so cancelling the request
//   cancels the reply.
// SPORT: internal.conversation.reply_rpc/ADDED (P1-CPA-16).

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The reply verbs.
const (
	MethodReplyOpen = "chat.reply_open"
	MethodReply     = "chat.reply"
)

var (
	_ = rpc.DeclareAccess(MethodReplyOpen, rpc.AccessOwnerView)
	_ = rpc.DeclareAccess(MethodReply, rpc.AccessOwnerView)
)

var (
	errReplyNoCaller       = cascade.New(cascade.KindPermissionDenied, "conversation: chat.reply needs a caller identity")
	errReplyParams         = cascade.New(cascade.KindInvalidInput, "conversation: malformed chat.reply params")
	errReplyThreadNotFound = cascade.New(cascade.KindNotFound, "conversation: reply thread not found")
	errReplyTurnNotFound   = cascade.New(cascade.KindNotFound, "conversation: reply turn not found")
	errReplyTurnOutside    = cascade.New(cascade.KindInvalidInput, "conversation: reply turn is not in this thread")
	errReplyNotUserTurn    = cascade.New(cascade.KindInvalidInput, "conversation: only a user turn can be replied to")
)

type replyOpenParams struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
}

type replyOpenResult struct {
	StreamID string `json:"stream_id"`
}

type replyParams struct {
	StreamID string `json:"stream_id"`
}

// RegisterHandlers registers chat.reply_open and chat.reply on registry.
func (r *Replier) RegisterHandlers(registry *rpc.Registry) {
	registry.Register(MethodReplyOpen, r.handleOpen)
	registry.Register(MethodReply, r.handleReply)
}

func (r *Replier) handleOpen(ctx context.Context, raw json.RawMessage) (any, error) {
	var p replyOpenParams
	if err := decodeStrict(raw, &p); err != nil {
		return nil, err
	}
	id, err := r.open(ctx, p.ThreadID, p.TurnID)
	if err != nil {
		return nil, err
	}
	return replyOpenResult{StreamID: id}, nil
}

func (r *Replier) handleReply(ctx context.Context, raw json.RawMessage) (any, error) {
	var p replyParams
	if err := decodeStrict(raw, &p); err != nil {
		return nil, err
	}
	res, err := r.Reply(ctx, ReplyRequest(p))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// decodeStrict decodes one JSON object into v, refusing unknown fields.
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errReplyParams
	}
	return nil
}

// open checks the target and mints a stream bound to the caller.
func (r *Replier) open(ctx context.Context, threadID, turnID string) (string, error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return "", err
	}
	if err := r.checkTarget(ctx, threadID, turnID); err != nil {
		return "", err
	}
	return r.cfg.Hub.open(threadID, turnID, caller)
}

// requireCaller returns the transport-verified caller on ctx.
func requireCaller(ctx context.Context) (principal.Caller, error) {
	c, ok := principal.CallerFrom(ctx)
	if !ok {
		return principal.Caller{}, errReplyNoCaller
	}
	return c, nil
}

// checkTarget admits a reply to turnID on threadID: the thread is in the
// caller's view and exists, and the turn is a user turn of that thread.
func (r *Replier) checkTarget(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errReplyParams
	}
	view, err := resolveView(ctx, r.cfg.Store, r.cfg.Clock, "")
	if err != nil {
		return err
	}
	if !view.admits(threadID) {
		return errScopeDenied
	}
	if _, ok, err := r.cfg.Store.GetThread(ctx, threadID); err != nil {
		return err
	} else if !ok {
		return errReplyThreadNotFound
	}
	tier, tierErr := r.cfg.Store.ThreadPrivacy(ctx, threadID)
	if tierErr != nil {
		return cascade.Wrap(cascade.KindUnavailable, tierErr, "conversation: chat.reply: privacy read failed")
	}
	if tier == provider.SensitivityLocalOnly && !servesLocalOnly(ctx) {
		return errReplyLocalOnly
	}
	ref, ok, err := r.sql.lookupTurn(ctx, turnID)
	switch {
	case err != nil:
		return err
	case !ok:
		return errReplyTurnNotFound
	case ref.ThreadID != threadID:
		return errReplyTurnOutside
	case ref.Role != RoleUser:
		return errReplyNotUserTurn
	}
	return nil
}

var errReplyLocalOnly = cascade.New(cascade.KindPermissionDenied,
	"conversation: chat.reply: local-only thread requires owner CLI")

// stream runs the main reply call and gathers its text, publishing each
// delta to stream id. A cancelled ctx wins over every other outcome: the
// partial text is dropped and KindCanceled returned.
func (r *Replier) stream(ctx context.Context, id string, req provider.ModelRequest) (string, error) {
	events, stop, err := r.door.ExecuteStream(ctx, req)
	if err != nil {
		return "", err
	}
	if stop != nil {
		defer stop()
	}
	var out strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", errReplyCancelled(ctx)
		case ev, open := <-events:
			switch {
			case ctx.Err() != nil:
				return "", errReplyCancelled(ctx)
			case !open:
				return "", errReplyTruncated
			case ev.Kind == provider.StreamEventDelta:
				out.WriteString(ev.Delta)
				r.cfg.Hub.publish(id, ev.Delta)
			case ev.Kind == provider.StreamEventDone:
				return out.String(), nil
			case ev.Kind == provider.StreamEventError:
				return "", errReplyProvider(ev.Err)
			}
		}
	}
}

// errReplyCancelled is the typed cancellation of a reply.
func errReplyCancelled(ctx context.Context) error {
	return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "conversation: reply cancelled; no assistant turn stored")
}

// errReplyProvider keeps a provider failure's kind (KindUnavailable when it has none).
func errReplyProvider(err error) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return cascade.Wrap(kind, err, "conversation: model stream failed; no assistant turn stored")
}

// persist stores text as the assistant turn answering b.turnID, with its
// mapping, through the journaled append in one store transaction.
func (r *Replier) persist(ctx context.Context, b replyBinding, text string) (string, error) {
	ctx = context.WithoutCancel(ctx) // the reply is complete; a late cancel must not tear the write
	turns, err := r.cfg.Store.ListTurns(ctx, b.threadID)
	if err != nil {
		return "", err
	}
	seq, now := int64(len(turns)), r.cfg.Clock.Now().Unix()
	turn := Turn{ID: NewTurnID(b.threadID, seq, RoleAssistant), ThreadID: b.threadID, Seq: seq,
		Role: RoleAssistant, CreatedAt: now, Origin: OriginLocal, Provenance: provider.ProvenanceTrusted}
	segs := []Segment{{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0,
		Kind: SegmentText, Content: text, CreatedAt: now}}
	p := journalPayload{Type: JournalEntryTurnAppend, Turn: &turn, Segments: segs,
		Provenance: turn.Provenance, ReplyTo: b.turnID}
	if err := appendJournaledPayload(ctx, r.cfg.Journal, p, func() error {
		return r.sql.appendReply(ctx, b.turnID, turn, segs)
	}); err != nil {
		return "", err
	}
	return turn.ID, nil
}

// tieredDoor is the reply door adapter: every call takes the thread tier
// from ThreadPrivacyFrom(ctx). No tier on ctx, or a non-zero request tier
// that differs from it, is refused before any dispatch.
type tieredDoor struct{ next ReplyDoor }

// Execute implements provider.ModelExecutor for the context stages.
func (d tieredDoor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	req, err := withThreadTier(ctx, req)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	return d.next.Execute(ctx, req)
}

// ExecuteStream is Execute's streaming twin for the main reply call.
func (d tieredDoor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	req, err := withThreadTier(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return d.next.ExecuteStream(ctx, req)
}

// withThreadTier sets req.Sensitivity to the thread tier on ctx.
func withThreadTier(ctx context.Context, req provider.ModelRequest) (provider.ModelRequest, error) {
	tp, ok := conductor.ThreadPrivacyFrom(ctx)
	if !ok || !tp.Mode.Valid() {
		return req, errDoorUntiered
	}
	if req.Sensitivity != provider.SensitivityTier(0) && req.Sensitivity != tp.Mode {
		return req, errDoorTierDiffers
	}
	req.Sensitivity = tp.Mode
	return req, nil
}
