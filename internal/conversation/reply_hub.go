package conversation

// Purpose: ReplyHub, the in-memory stream a chat.reply's deltas travel on
//   to the client. Never the events bus: reply content is never logged.
// Inputs: chat.reply_open binds a server-minted stream id to the caller
//   and the thread; chat.reply claims it once and publishes frames.
// Outputs: Frame values for the SSE topic "chat.reply" (P1-CPA-13).
// Constraints: a stream id is 128 random bits, bound to one caller
//   identity and one thread, single use, and expires 60 s after it was
//   opened if never claimed. A claimed stream is buffered from its first
//   frame to its terminal frame, kept 60 s after the terminal frame for
//   late subscribers, then dropped with its buffer (R17 M-1, R17c N-10).
// SPORT: internal.conversation.reply_hub/ADDED (P1-CPA-16).

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// replyStreamTTL is how long an opened, unclaimed stream id lives.
	replyStreamTTL = 60 * time.Second
	// replyStreamGrace is how long a finished stream stays subscribable.
	replyStreamGrace = 60 * time.Second
	// maxReplyStreams bounds the live streams one hub holds.
	maxReplyStreams = 1024
	// subscribeBuffer is the per-subscriber channel buffer.
	subscribeBuffer = 16
)

// The frame types a reply stream carries.
const (
	FrameDelta = "delta"
	FrameDone  = "done"
	FrameError = "error"
)

var (
	errStreamNotFound = cascade.New(cascade.KindNotFound, "conversation: reply stream not found or expired")
	errStreamForeign  = cascade.New(cascade.KindPermissionDenied, "conversation: reply stream belongs to another caller")
	errStreamUsed     = cascade.New(cascade.KindConflict, "conversation: reply stream already used")
	errStreamsFull    = cascade.New(cascade.KindUnavailable, "conversation: too many open reply streams")
	errHubNilClock    = cascade.New(cascade.KindInvalidInput, "conversation: NewReplyHub requires a non-nil Clock")
)

// Frame is one event on a reply stream. A delta frame carries text; the
// terminal done or error frame carries none (error names only the kind).
type Frame struct {
	Type     string `json:"type"`
	Delta    string `json:"delta,omitempty"`
	Kind     string `json:"kind,omitempty"`
	ThreadID string `json:"thread_id"`
}

// ReplyHub holds the process's reply streams. One per daemon, shared by
// the Replier and the SSE topic.
type ReplyHub struct {
	clock   Clock
	mu      sync.Mutex
	streams map[string]*replyStream
}

// replyStream is one stream's binding and buffer.
type replyStream struct {
	threadID, turnID, caller string
	openedAt, endedAt        time.Time
	claimed, terminal        bool
	frames                   []Frame
	wake                     chan struct{}
}

// replyBinding is what a claim hands chat.reply.
type replyBinding struct {
	id, threadID, turnID, caller string
}

// NewReplyHub returns an empty hub reading time from clock.
func NewReplyHub(clock Clock) (*ReplyHub, error) {
	if clock == nil {
		return nil, errHubNilClock
	}
	return &ReplyHub{clock: clock, streams: map[string]*replyStream{}}, nil
}

// callerIdentity is the binding key of c: the principal id, else the owner CLI or owner view.
func callerIdentity(c principal.Caller) string {
	switch {
	case c.Principal != nil:
		return "principal:" + c.Principal.ID
	case c.OwnerCLI:
		return "owner-cli"
	default:
		return "owner"
	}
}

// open mints a stream id bound to c, threadID and turnID.
func (h *ReplyHub) open(threadID, turnID string, c principal.Caller) (string, error) {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "conversation: mint reply stream id")
	}
	id := hex.EncodeToString(raw[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock.Now()
	h.sweepLocked(now)
	if len(h.streams) >= maxReplyStreams {
		return "", errStreamsFull
	}
	h.streams[id] = &replyStream{threadID: threadID, turnID: turnID, caller: callerIdentity(c),
		openedAt: now, wake: make(chan struct{})}
	return id, nil
}

// claim takes stream id for one chat.reply by c: unknown or expired is
// KindNotFound, another caller's KindPermissionDenied, a reuse
// KindConflict. A refused claim leaves the stream as it was.
func (h *ReplyHub) claim(id string, c principal.Caller) (replyBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked(h.clock.Now())
	s, ok := h.streams[id]
	switch {
	case !ok:
		return replyBinding{}, errStreamNotFound
	case s.caller != callerIdentity(c):
		return replyBinding{}, errStreamForeign
	case s.claimed:
		return replyBinding{}, errStreamUsed
	}
	s.claimed = true
	return replyBinding{id: id, threadID: s.threadID, turnID: s.turnID, caller: s.caller}, nil
}

// publish appends one delta to stream id; a finished stream takes none.
func (h *ReplyHub) publish(id, delta string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.streams[id]; ok && !s.terminal {
		s.frames = append(s.frames, Frame{Type: FrameDelta, Delta: delta, ThreadID: s.threadID})
		s.wakeLocked()
	}
}

// finish writes stream id's terminal frame: done on a nil err, else an
// error frame naming only err's kind.
func (h *ReplyHub) finish(id string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[id]
	if !ok || s.terminal {
		return
	}
	f := Frame{Type: FrameDone, ThreadID: s.threadID}
	if err != nil {
		kind, known := cascade.KindOf(err)
		if !known {
			kind = cascade.KindInternal
		}
		f = Frame{Type: FrameError, Kind: kind.String(), ThreadID: s.threadID}
	}
	s.frames = append(s.frames, f)
	s.terminal, s.endedAt = true, h.clock.Now()
	s.wakeLocked()
}

// Subscribe returns stream id's frames for c from the first one, closed
// after the terminal frame or when ctx ends. Unknown or dropped is
// KindNotFound; a stream bound to another caller is KindPermissionDenied.
func (h *ReplyHub) Subscribe(ctx context.Context, streamID string, c principal.Caller) (<-chan Frame, error) {
	h.mu.Lock()
	h.sweepLocked(h.clock.Now())
	s, ok := h.streams[streamID]
	var err error
	switch {
	case !ok:
		err = errStreamNotFound
	case s.caller != callerIdentity(c):
		err = errStreamForeign
	}
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make(chan Frame, subscribeBuffer)
	go h.pump(ctx, s, out)
	return out, nil
}

// pump copies s's frames to out as they arrive.
func (h *ReplyHub) pump(ctx context.Context, s *replyStream, out chan<- Frame) {
	defer close(out)
	next := 0
	for {
		h.mu.Lock()
		next = min(next, len(s.frames)) // a dropped stream's buffer is released
		pending := append([]Frame(nil), s.frames[next:]...)
		done, wake := s.terminal, s.wake
		h.mu.Unlock()
		for _, f := range pending {
			select {
			case out <- f:
			case <-ctx.Done():
				return
			}
		}
		next += len(pending)
		if done {
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

// wakeLocked tells every pump a frame arrived.
func (s *replyStream) wakeLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// sweepLocked drops unclaimed streams past their TTL and finished streams
// past their grace, releasing each buffer.
func (h *ReplyHub) sweepLocked(now time.Time) {
	for id, s := range h.streams {
		expired := !s.claimed && now.Sub(s.openedAt) >= replyStreamTTL
		drained := s.terminal && now.Sub(s.endedAt) >= replyStreamGrace
		if expired || drained {
			if !s.terminal {
				s.terminal = true
				s.wakeLocked() // a subscriber to an expired stream ends now
			}
			s.frames = nil
			delete(h.streams, id)
		}
	}
}
