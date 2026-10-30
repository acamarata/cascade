// Purpose: the platform-independent half of clipboard delivery: the
//
//	ClipboardWriter contract, approval gating, the effect-recorded write and
//	the hold that blocks until the TTL clear has run and been confirmed.
//	Platform pbcopy/xclip/Windows specifics live in the three build-tagged
//	sibling files; the durable clear and the daemon re-arm live in
//	clipboard_rearm.go. This file never imports os/exec.
//
// Inputs: a signed approval, a payload, an injected runtime.Clock, an
//
//	audit.Writer, an audit.EffectWriter, a PendingClearStore and a
//	ClipboardScheduler.
//
// Outputs: ClipboardWriter, NewClipboardWriter, the clipboard sentinels.
//
// Constraints: FAIL CLOSED. Order in Write: verify approval, BeginEffect
//
//	(intent), store.Put, setValue, ConfirmEffect; any failure before setValue
//	stops with the clipboard untouched, and after setValue a failure clears
//	and keeps the row until a confirmed clear. No payload byte reaches an
//	audit record, a log or an argument vector. No time.Sleep: the hold waits
//	on the injected scheduler.
//
// SPORT: internal/secrets ClipboardWriter/CHANGED, NewClipboardWriter/CHANGED,
//
//	ErrClipboardUnsupported/ADDED (P1-SEC-09).

package secrets

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// KindSecretsClipboardWrite is the audit kind every clipboard record
// carries. It aliases the one declaration in internal/audit.
const KindSecretsClipboardWrite = audit.KindSecretsClipboardWrite

// ClipboardApprovalVerb is the only verb an approval presented to Write
// may name.
const ClipboardApprovalVerb = "vault.get.clipboard"

const (
	clipboardWriteAction   = "clipboard.write"
	clipboardClearAction   = "clipboard.clear"
	clipboardRefusedAction = "clipboard.write.refused"
	clipboardActor         = "cli"
)

// Sentinel errors, each carrying its own taxonomy Kind.
var (
	// ErrTier2Unsupported: Windows is tier-2 scope; no clipboard service.
	ErrTier2Unsupported = cascade.New(cascade.KindUnsupported,
		"secrets: clipboard delivery requires macOS or Linux; on Windows use `cascade vault get` and copy manually")
	// ErrClipboardUnsupported: this host has no clipboard tool (the
	// exec.LookPath probe failed). Nothing is written or persisted.
	ErrClipboardUnsupported = cascade.New(cascade.KindUnsupported,
		"secrets: no clipboard tool is installed on this host (install xclip on Linux); nothing was written")
	// ErrClipboardUnavailable: the tool is present but failed.
	ErrClipboardUnavailable = cascade.New(cascade.KindUnavailable,
		"secrets: the clipboard tool failed")
	// ErrApprovalRequired: no verifiable approval was presented.
	ErrApprovalRequired = cascade.New(cascade.KindElevationRequired,
		"secrets: clipboard delivery requires a verified approval")
)

// ApprovalVerifier is the approval-record verify seam
// (policy.ApprovalVerifier in production).
type ApprovalVerifier interface {
	Verify(signed []byte) (*policy.ApprovalRecord, error)
}

// clipboardOps is the per-platform surface; exactly one newClipboardOps
// compiles into any build. probe reports ErrClipboardUnsupported (or the
// tier-2 refusal) when the host has no tool, before anything is recorded.
type clipboardOps interface {
	platform() string
	probe() error
	setValue(ctx context.Context, payload []byte) error
	clearValue(ctx context.Context) error
}

// clipboardEffects is the effect writer plus the unknown-outcome mark a
// failed clear needs. *audit.EffectLog provides both.
type clipboardEffects interface {
	audit.EffectWriter
	MarkUnknownOutcome(ctx context.Context, h audit.EffectHandle) (audit.Record, error)
}

// ClipboardWriter is the gated clipboard delivery channel.
type ClipboardWriter interface {
	// Write verifies signedApproval, records the intent, persists the
	// pending clear, places payload on the clipboard, confirms, then blocks
	// until the TTL clear has run and been confirmed. Cancelling ctx (the
	// SIGINT/SIGTERM path) clears at once.
	Write(ctx context.Context, signedApproval []byte, payload []byte) error
}

type clipboardWriter struct {
	clipboardClearer
	audit     audit.Writer
	verifier  ApprovalVerifier
	scheduler ClipboardScheduler
	ttl       time.Duration
}

// NewClipboardWriter builds the production ClipboardWriter over this host's
// clipboard tool. Every dependency is required, effects must also mark an
// unknown outcome, and ttl must lie in [5s, 10m].
func NewClipboardWriter(clock runtime.Clock, aw audit.Writer, effects audit.EffectWriter, verifier ApprovalVerifier,
	store PendingClearStore, scheduler ClipboardScheduler, ttl time.Duration) (ClipboardWriter, error) {
	ops, err := newClipboardOps()
	if err != nil {
		return nil, err
	}
	return newClipboardWriterWithOps(clock, aw, effects, verifier, store, scheduler, ops, ttl)
}

// newClipboardWriterWithOps is the fully injected constructor tests use to
// substitute a fake backend.
func newClipboardWriterWithOps(clock runtime.Clock, aw audit.Writer, effects audit.EffectWriter, verifier ApprovalVerifier,
	store PendingClearStore, scheduler ClipboardScheduler, ops clipboardOps, ttl time.Duration) (ClipboardWriter, error) {
	if clock == nil || aw == nil || effects == nil || verifier == nil || store == nil || scheduler == nil || ops == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: clipboard writer requires a clock, audit writer, effect writer, approval verifier, store, scheduler and platform ops")
	}
	fx, ok := effects.(clipboardEffects)
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: clipboard writer requires an effect writer that can mark an unknown outcome")
	}
	if err := checkClipboardTTL(ttl); err != nil {
		return nil, err
	}
	return &clipboardWriter{
		clipboardClearer: clipboardClearer{clock: clock, effects: fx, store: store, ops: ops},
		audit:            aw, verifier: verifier, scheduler: scheduler, ttl: ttl,
	}, nil
}

// clipboardEventFields is the zero-payload explain of every clipboard
// record: the action, an opaque ref, the timestamps and the platform.
type clipboardEventFields struct {
	ActionClass string `json:"action_class"`
	Ref         string `json:"ref,omitempty"`
	WrittenAt   string `json:"written_at,omitempty"`
	ClearDueAt  string `json:"clear_due_at,omitempty"`
	Platform    string `json:"platform"`
}

func effectRequest(action, key string, row PendingClear) audit.EffectRequest {
	fields := clipboardEventFields{ActionClass: action, Ref: row.RefID, Platform: row.Platform}
	if !row.WrittenAt.IsZero() {
		fields.WrittenAt = row.WrittenAt.Format(time.RFC3339Nano)
		fields.ClearDueAt = row.ClearDueAt.Format(time.RFC3339Nano)
	}
	explain, _ := json.Marshal(fields) // a struct of strings always encodes
	return audit.EffectRequest{Kind: KindSecretsClipboardWrite, Actor: clipboardActor, Action: action, Key: key, Explain: explain}
}

// Write implements ClipboardWriter.
func (w *clipboardWriter) Write(ctx context.Context, signedApproval []byte, payload []byte) error {
	if err := w.approve(ctx, signedApproval); err != nil {
		return err
	}
	if err := w.ops.probe(); err != nil {
		return err
	}
	id, err := cascade.NewID()
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: the clipboard write could not be named; nothing was written")
	}
	now := w.clock.Now().UTC()
	row := PendingClear{RefID: id.String(), Platform: w.ops.platform(), WrittenAt: now, ClearDueAt: now.Add(w.ttl)}
	h, err := w.effects.BeginEffect(ctx, effectRequest(clipboardWriteAction, clipboardWriteAction+":"+row.RefID, row))
	if err != nil {
		return w.abandon(ctx, h, cascade.Wrap(cascade.KindUnavailable, err, "secrets: the clipboard write could not be audited; nothing was written"))
	}
	if err := w.store.Put(ctx, row); err != nil {
		return w.abandon(ctx, h, cascade.Wrap(kindOr(err, cascade.KindUnavailable), err,
			"secrets: the pending clear could not be persisted; nothing was written"))
	}
	if err := w.ops.setValue(ctx, payload); err != nil {
		_, merr := w.effects.MarkUnknownOutcome(ctx, h)
		return withCleanup(setValueError(err), merr, w.clear(context.WithoutCancel(ctx), row.RefID))
	}
	if _, err := w.effects.ConfirmEffect(ctx, h, "on the clipboard"); err != nil {
		// Clear at once rather than at the TTL. The message must not claim a
		// clear that did not complete.
		cerr := w.clear(context.WithoutCancel(ctx), row.RefID)
		msg := "secrets: the clipboard write could not be confirmed; the value was cleared at once"
		if cerr != nil {
			msg = "secrets: the clipboard write could not be confirmed and the immediate clear did not complete; the pending clear is kept for the daemon"
		}
		return withCleanup(cascade.Wrap(cascade.KindUnavailable, err, msg), cerr)
	}
	return w.hold(ctx, row.RefID)
}

// approve verifies the approval and its verb. A refusal is audited; an
// audit failure is reported with the refusal, never instead of it.
func (w *clipboardWriter) approve(ctx context.Context, signed []byte) error {
	rec, err := w.verifier.Verify(signed)
	if err == nil && (rec == nil || rec.Verb != ClipboardApprovalVerb) {
		err = cascade.Newf(cascade.KindElevationRequired, "the approval does not name %s", ClipboardApprovalVerb)
	}
	if err == nil {
		return nil
	}
	refusal := cascade.Wrap(cascade.KindElevationRequired, ErrApprovalRequired, "secrets: clipboard delivery refused: "+err.Error())
	explain, _ := json.Marshal(clipboardEventFields{ActionClass: clipboardRefusedAction, Platform: w.ops.platform()})
	_, aerr := w.audit.Append(ctx, audit.Event{Kind: KindSecretsClipboardWrite, Actor: clipboardActor, Action: clipboardRefusedAction, Explain: explain})
	return withCleanup(refusal, aerr)
}

// abandon fails an intent whose effect never ran (h is zero when no intent
// was committed) and returns cause.
func (w *clipboardWriter) abandon(ctx context.Context, h audit.EffectHandle, cause error) error {
	if h.IntentSeq == 0 {
		return cause
	}
	_, err := w.effects.FailEffect(ctx, h, "not run: "+cause.Error())
	return withCleanup(cause, err)
}

// hold blocks until the scheduled clear has run, or clears at once when ctx
// ends. Exactly one of the two paths runs the clear.
func (w *clipboardWriter) hold(ctx context.Context, ref string) error {
	var claimed atomic.Bool
	done := make(chan error, 1)
	stop := w.scheduler.AfterFunc(w.ttl, func() {
		if claimed.CompareAndSwap(false, true) {
			done <- w.clear(context.Background(), ref)
		}
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	if stop != nil {
		stop()
	}
	if !claimed.CompareAndSwap(false, true) {
		return <-done
	}
	return w.clear(context.WithoutCancel(ctx), ref)
}

// setValueError keeps a taxonomy error from the backend and promotes any
// other OS error to ErrClipboardUnavailable.
func setValueError(err error) error {
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, err.Error())
}

func kindOr(err error, fallback cascade.Kind) cascade.Kind {
	if k, ok := cascade.KindOf(err); ok {
		return k
	}
	return fallback
}

// withCleanup returns primary (keeping its kind and chain), naming every
// non-nil later error from the cleanup that followed it.
func withCleanup(primary error, more ...error) error {
	var also []string
	for _, err := range more {
		if err != nil {
			also = append(also, err.Error())
		}
	}
	if len(also) == 0 {
		return primary
	}
	return cascade.Wrap(kindOr(primary, cascade.KindInternal), primary, "and then: "+strings.Join(also, "; "))
}
