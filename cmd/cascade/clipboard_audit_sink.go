// Purpose: the clipboard writer's audit sink, shaped like autoGateSink. When
//
//	a daemon answers, every record goes through its owner-only
//	audit.clipboard_effect verb into the daemon's one audit log; when no
//	daemon answers, each record opens the runtime store, writes through the
//	process log and closes it again, so the store (which admits one opener)
//	is never held across the gate's own rows or the TTL.
//
// Inputs: the profile paths and clock; dial and openDirect are test seams.
// Outputs: clipboardAuditSink (audit.Writer, audit.EffectWriter and
//
//	MarkUnknownOutcome), newClipboardAuditSink.
//
// Constraints: the mode is chosen once, on the first record, and kept, so
//
//	an intent and its terminal record land in the same log. Only a failure
//	to reach the socket at all falls back to the direct path; any other
//	daemon error refuses. Every request is validated before it is sent.
//
// SPORT: cmd/cascade clipboard audit sink (ADD, P1-SEC-09, P1-BF-R298).
package main

import (
	"context"
	"reflect"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clipboardDirect applies one request to a freshly opened log.
type clipboardDirect func(ctx context.Context, p clipboardEffectParams) (clipboardEffectAck, error)

type clipboardAuditSink struct {
	paths runtime.PathProvider
	clock runtime.Clock
	// dial returns the daemon caller; nil uses the profile's socket.
	dial func() (gateCall, error)
	// openDirect is the per-record store path; nil opens the runtime store.
	openDirect clipboardDirect

	mu   sync.Mutex
	mode int
	call gateCall
}

func newClipboardAuditSink(paths runtime.PathProvider, clock runtime.Clock) *clipboardAuditSink {
	return &clipboardAuditSink{paths: paths, clock: clock}
}

// do validates p and sends it to the daemon or the store.
func (s *clipboardAuditSink) do(ctx context.Context, p clipboardEffectParams) (clipboardEffectAck, error) {
	if err := p.validate(); err != nil {
		return clipboardEffectAck{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.mode {
	case gateModeDaemon:
		return clipboardViaDaemon(ctx, s.call, p)
	case gateModeDirect:
		return s.direct(ctx, p)
	}
	call, err := s.daemonCaller()
	if err != nil {
		return clipboardEffectAck{}, err
	}
	ack, err := clipboardViaDaemon(ctx, call, p)
	if err == nil {
		s.mode, s.call = gateModeDaemon, call
		return ack, nil
	}
	if !isDialFailure(err) {
		return clipboardEffectAck{}, err
	}
	s.mode = gateModeDirect
	return s.direct(ctx, p)
}

func clipboardViaDaemon(ctx context.Context, call gateCall, p clipboardEffectParams) (clipboardEffectAck, error) {
	var ack clipboardEffectAck
	if err := call(ctx, clipboardEffectMethod, p, &ack); err != nil {
		return clipboardEffectAck{}, err
	}
	if ack.Seq == 0 || (p.Op == clipOpBegin && (ack.Handle == nil || ack.Handle.Key != p.Key || ack.Handle.IntentSeq != ack.Seq)) {
		return clipboardEffectAck{}, cascade.New(cascade.KindUnavailable, "clipboard audit: the daemon did not acknowledge the record")
	}
	return ack, nil
}

func (s *clipboardAuditSink) daemonCaller() (gateCall, error) {
	if s.dial != nil {
		return s.dial()
	}
	settings, err := daemon.ResolveSettings(nil, s.paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(client.UnixDialer), 5*time.Second).Do, nil
}

// direct opens the store for this one record and closes it again.
func (s *clipboardAuditSink) direct(ctx context.Context, p clipboardEffectParams) (clipboardEffectAck, error) {
	if s.openDirect != nil {
		return s.openDirect(ctx, p)
	}
	if s.paths == nil {
		return clipboardEffectAck{}, cascade.New(cascade.KindUnavailable, "clipboard audit: no profile paths are configured")
	}
	store, closeStore, err := openPluginStore(ctx, s.paths, s.clock)
	if err != nil {
		return clipboardEffectAck{}, cascade.Wrap(cascade.KindUnavailable, err, "clipboard audit: no daemon answered and the store could not be opened")
	}
	defer closeStore()
	log, err := openAuditLog(store, s.clock, nil, s.paths)
	if err != nil {
		return clipboardEffectAck{}, err
	}
	fx, err := openEffectLog(store, s.clock, nil, s.paths)
	if err != nil {
		return clipboardEffectAck{}, err
	}
	return applyClipboardEffect(ctx, log, fx, p)
}

func (s *clipboardAuditSink) terminal(ctx context.Context, op string, h audit.EffectHandle, detail string) (audit.Record, error) {
	if h.Kind != secrets.KindSecretsClipboardWrite {
		return audit.Record{}, clipInvalid("the handle is not a clipboard intent")
	}
	ack, err := s.do(ctx, clipboardEffectParams{Op: op, Detail: clipboardDetail(detail),
		Handle: &clipboardHandle{Key: h.Key, IntentSeq: h.IntentSeq, IntentID: h.IntentID}})
	return audit.Record{Seq: ack.Seq}, err
}

// clipboardDetail fits a terminal record's detail to the verb's vocabulary:
// control characters become spaces and the text is cut, on a rune boundary,
// to clipboardDetailMax bytes. A writer's abandon reason carries a whole
// error chain, and the record must still land rather than be refused.
func clipboardDetail(s string) string {
	b := make([]byte, 0, min(len(s), clipboardDetailMax))
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		if len(b)+utf8.RuneLen(r) > clipboardDetailMax {
			break
		}
		b = utf8.AppendRune(b, r)
	}
	return string(b)
}

// Append writes the clipboard refusal row, the only event the writer appends.
func (s *clipboardAuditSink) Append(ctx context.Context, e audit.Event) (audit.Record, error) {
	rest := e
	rest.Kind, rest.Actor, rest.Action, rest.Explain = "", "", "", nil
	if e.Kind != secrets.KindSecretsClipboardWrite || e.Actor != clipActor || !reflect.DeepEqual(rest, audit.Event{}) {
		return audit.Record{}, clipInvalid("only the clipboard refusal row is accepted")
	}
	ack, err := s.do(ctx, clipboardEffectParams{Op: clipOpRefused, Action: e.Action, Explain: e.Explain})
	return audit.Record{Seq: ack.Seq}, err
}

// BeginEffect implements audit.EffectWriter.
func (s *clipboardAuditSink) BeginEffect(ctx context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	if req.Kind != secrets.KindSecretsClipboardWrite || req.Actor != clipActor || req.ParamsHash != "" {
		return audit.EffectHandle{}, clipInvalid("only a clipboard effect is accepted")
	}
	ack, err := s.do(ctx, clipboardEffectParams{Op: clipOpBegin, Action: req.Action, Key: req.Key, Explain: req.Explain})
	if err != nil {
		return audit.EffectHandle{}, err
	}
	return ack.Handle.effect(), nil
}

// ConfirmEffect implements audit.EffectWriter.
func (s *clipboardAuditSink) ConfirmEffect(ctx context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	return s.terminal(ctx, clipOpConfirm, h, outcome)
}

// FailEffect implements audit.EffectWriter.
func (s *clipboardAuditSink) FailEffect(ctx context.Context, h audit.EffectHandle, reason string) (audit.Record, error) {
	return s.terminal(ctx, clipOpFail, h, reason)
}

// MarkUnknownOutcome settles an intent whose effect may or may not have run.
func (s *clipboardAuditSink) MarkUnknownOutcome(ctx context.Context, h audit.EffectHandle) (audit.Record, error) {
	return s.terminal(ctx, clipOpUnknown, h, "")
}
