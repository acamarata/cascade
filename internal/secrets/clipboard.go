// Purpose: the platform-independent half of the clipboard fallback: the
//
//	ClipboardWriter contract, approval gating, the zero-payload audit
//	contract, and the durable pending-clear record that survives a daemon
//	restart. Platform pbcopy/xclip/Windows-refusal specifics live in the
//	three build-tagged sibling files; this file never imports os/exec.
//
// Inputs: a signed approval token, a payload, an injected runtime.Clock,
//
//	an audit.Writer, a PendingClearStore and a ClipboardScheduler.
//
// Outputs: ClipboardWriter, NewClipboardWriter, ErrTier2Unsupported,
//
//	ErrClipboardUnavailable, ErrApprovalRequired, PendingClearStore,
//	RearmPendingClears.
//
// Constraints: FAIL CLOSED — an unverifiable token, an unavailable
//
//	platform tool, or a store that cannot persist the pending clear all
//	refuse before a byte reaches an OS clipboard. No payload byte is ever
//	handed to audit.Writer, logged, or placed in a subprocess argument
//	vector. No bare time.After/time.Sleep in this file or its siblings;
//	scheduling goes through ClipboardScheduler, every timestamp comes from
//	the injected Clock. Contract-vs-tree contradictions (the audit Kind
//	enum gap, the ApprovalToken/ApprovalVerifier mismatch, the append-only
//	"same record" requirement) are recorded in the ticket journal, not
//	here, to stay inside the 300-line cap.
//
// SPORT: internal/secrets ClipboardWriter/ADDED, NewClipboardWriter/ADDED,
//
//	ErrTier2Unsupported/ADDED, ErrClipboardUnavailable/ADDED,
//	ErrApprovalRequired/ADDED, PendingClearStore/ADDED,
//	RearmPendingClears/ADDED (P1-E08-W2-S16-T4).

package secrets

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clipboardClearTTL is the 08 S3 [secrets] clipboard_ttl default.
const clipboardClearTTL = 30 * time.Second

// KindSecretsClipboardWrite is the audit kind this package emits under.
// R-21.235 requires it as a ratified internal/audit.Kind value; as of
// this ticket the enum (internal/audit/schema.go, owned by
// P1-E09-W2-S18-T2, outside this ticket's files_scope) is still closed at
// its original eleven names and has none with a "secrets." prefix. This
// constant carries the exact string R-21.235 specifies, valid once T2
// lands; see the journal for the full contradiction.
const KindSecretsClipboardWrite audit.Kind = "secrets.clipboard_write"

const clipboardActionClass = "clipboard.write"

// Sentinel errors, each carrying its own taxonomy Kind.
var (
	// ErrTier2Unsupported: Windows is tier-2 scope; no clipboard service.
	ErrTier2Unsupported = cascade.New(cascade.KindUnsupported,
		"secrets: clipboard delivery requires macOS or Linux; on Windows use `cascade vault get` and copy manually")
	// ErrClipboardUnavailable: the platform tool cannot be reached.
	// Fail closed (R-14.24): no alternative delivery path.
	ErrClipboardUnavailable = cascade.New(cascade.KindUnavailable,
		"secrets: no OS clipboard tool is available (install xclip on Linux); nothing was written")
	// ErrApprovalRequired: no verifiable approval token was presented.
	ErrApprovalRequired = cascade.New(cascade.KindElevationRequired,
		"secrets: clipboard delivery requires a verified approval token")
)

// ApprovalVerifier is the H/S-16.T3 verify seam. The ticket text names
// Write's gating parameter `tok policy.ApprovalToken`, a type with no
// signature field; R-21.242 also requires "the H/S-16.T3 Verify path
// (signature, expiry, domain separation)", which the tree implements as
// policy.ApprovalVerifier.Verify(signed []byte) (*policy.ApprovalRecord,
// error). This file follows the mechanism the tree actually ships. See
// the journal for both sides quoted.
type ApprovalVerifier interface {
	Verify(signed []byte) (*policy.ApprovalRecord, error)
}

// PendingClear is the durable, restart-surviving record of one clipboard
// write awaiting its clear (R-21.242): no payload, hash or byte length.
type PendingClear struct {
	RefID      string
	Platform   string
	WrittenAt  time.Time
	ClearDueAt time.Time
}

// PendingClearStore persists PendingClear rows. The spec places these in
// the `policy` storage domain; this ticket's files_scope (internal/secrets
// plus two docs files) excludes internal/policy and the cross-domain
// capability grant a real implementation would need, so this is the seam
// a composition root supplies. See the journal.
type PendingClearStore interface {
	Put(ctx context.Context, pc PendingClear) error
	List(ctx context.Context) ([]PendingClear, error)
	Delete(ctx context.Context, refID string) error
}

// ClipboardScheduler abstracts the one-shot post-write countdown so tests
// fire it deterministically. NewSystemClipboardScheduler is the
// production adapter and the one place time.AfterFunc appears (mirroring
// internal/runtime.Ticker's systemTicker); domain logic only ever sees
// this interface, and every recorded timestamp still comes from Clock.
type ClipboardScheduler interface {
	AfterFunc(d time.Duration, fn func()) (stop func())
}

type systemClipboardScheduler struct{}

// NewSystemClipboardScheduler returns the production scheduler.
func NewSystemClipboardScheduler() ClipboardScheduler { return systemClipboardScheduler{} }

func (systemClipboardScheduler) AfterFunc(d time.Duration, fn func()) (stop func()) {
	t := time.AfterFunc(d, fn)
	return func() { t.Stop() }
}

// clipboardOps is the per-platform surface. clipboard_darwin.go,
// clipboard_linux.go and clipboard_windows.go each define newClipboardOps
// under their own //go:build tag; exactly one compiles into any build.
type clipboardOps interface {
	platform() string
	setValue(ctx context.Context, payload []byte) error
	clearValue(ctx context.Context) error
}

// ClipboardWriter is the gated clipboard delivery channel (R-21.242).
type ClipboardWriter interface {
	// Write verifies signedApproval, places payload on the OS clipboard,
	// and arms a TTL-bounded durable clear. Refuses with
	// ErrApprovalRequired (no subprocess run) if the token does not
	// verify; ErrTier2Unsupported on Windows before any OS call;
	// ErrClipboardUnavailable if the tool cannot be reached (nothing
	// written).
	Write(ctx context.Context, signedApproval []byte, payload []byte) error
}

type clipboardWriter struct {
	clock     runtime.Clock
	audit     audit.Writer
	verifier  ApprovalVerifier
	store     PendingClearStore
	scheduler ClipboardScheduler
	ops       clipboardOps
	ttl       time.Duration
}

// NewClipboardWriter builds the production ClipboardWriter for this host's
// platform. Every dependency is required: this is security-class code and
// a writer missing a seam cannot honor its guarantees, so construction
// refuses rather than degrading silently.
func NewClipboardWriter(
	clock runtime.Clock, aw audit.Writer, verifier ApprovalVerifier,
	store PendingClearStore, scheduler ClipboardScheduler,
) (ClipboardWriter, error) {
	ops, err := newClipboardOps()
	if err != nil {
		return nil, err
	}
	return newClipboardWriterWithOps(clock, aw, verifier, store, scheduler, ops)
}

// newClipboardWriterWithOps is the fully-injected constructor the
// platform test files use to substitute a fake clipboardOps.
func newClipboardWriterWithOps(
	clock runtime.Clock, aw audit.Writer, verifier ApprovalVerifier,
	store PendingClearStore, scheduler ClipboardScheduler, ops clipboardOps,
) (ClipboardWriter, error) {
	if clock == nil || aw == nil || verifier == nil || store == nil || scheduler == nil || ops == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: clipboard writer requires a clock, audit writer, approval verifier, store, scheduler and platform ops")
	}
	return &clipboardWriter{
		clock: clock, audit: aw, verifier: verifier, store: store,
		scheduler: scheduler, ops: ops, ttl: clipboardClearTTL,
	}, nil
}

// clipboardEventFields is the zero-payload JSON shape stamped into every
// audit Event's Explain field: action_class, an opaque ref, written_at,
// cleared_at (empty until the clear fires), and platform. Never anything
// derived from the payload.
type clipboardEventFields struct {
	ActionClass string `json:"action_class"`
	Ref         string `json:"ref"`
	WrittenAt   string `json:"written_at"`
	ClearedAt   string `json:"cleared_at,omitempty"`
	Platform    string `json:"platform"`
}

// Write implements ClipboardWriter.
func (w *clipboardWriter) Write(ctx context.Context, signedApproval []byte, payload []byte) error {
	if _, err := w.verifier.Verify(signedApproval); err != nil {
		return cascade.Wrap(cascade.KindElevationRequired, ErrApprovalRequired, err.Error())
	}
	if err := w.ops.setValue(ctx, payload); err != nil {
		// A taxonomy error setValue already produced (Windows tier-2's
		// ErrTier2Unsupported, in particular) is returned as-is: wrapping
		// it again in ErrClipboardUnavailable would bury its Kind behind
		// a second cascade.Error, and errors.Is(err, ErrTier2Unsupported)
		// stops matching once that happens. Only a genuine non-taxonomy
		// OS error (the real POSIX clipboard-tool-missing case) gets
		// promoted to ErrClipboardUnavailable here.
		if _, ok := cascade.KindOf(err); ok {
			return err
		}
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, err.Error())
	}
	refID, err := cascade.NewID()
	if err != nil {
		_ = w.ops.clearValue(ctx)
		return cascade.Wrap(cascade.KindInternal, err, "secrets: clipboard write could not be recorded")
	}
	ref := refID.String()
	writtenAt := w.clock.Now().UTC()
	dueAt := writtenAt.Add(w.ttl)
	if err := w.store.Put(ctx, PendingClear{
		RefID: ref, Platform: w.ops.platform(), WrittenAt: writtenAt, ClearDueAt: dueAt,
	}); err != nil {
		_ = w.ops.clearValue(ctx)
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: clipboard clear could not be scheduled durably")
	}
	w.emitAudit(ctx, ref, writtenAt, time.Time{})
	w.scheduler.AfterFunc(w.ttl, func() { w.fireClear(context.Background(), ref) })
	return nil
}

// fireClear runs the platform clear, updates the audit trail, and drops
// the pending-clear row. Called from the armed scheduler and from
// RearmPendingClears on daemon start.
func (w *clipboardWriter) fireClear(ctx context.Context, ref string) {
	// The AUDIT CONTRACT requires the cleared_at update regardless of
	// subprocess error status, so the outcome itself is not branched on.
	_ = w.ops.clearValue(ctx)
	clearedAt := w.clock.Now().UTC()
	w.emitAudit(ctx, ref, time.Time{}, clearedAt)
	_ = w.store.Delete(ctx, ref)
}

// emitAudit appends one clipboard.write audit event. See
// KindSecretsClipboardWrite and the journal for the closed-enum gap, and
// the journal for why this appends a second event at clear time (with the
// same ref) rather than mutating the first: internal/audit.Log's whole
// method set is Append/Query/Explain/Verify, so a sealed record cannot be
// updated in place.
func (w *clipboardWriter) emitAudit(ctx context.Context, ref string, writtenAt, clearedAt time.Time) {
	fields := clipboardEventFields{ActionClass: clipboardActionClass, Ref: ref, Platform: w.ops.platform()}
	if !writtenAt.IsZero() {
		fields.WrittenAt = writtenAt.Format(time.RFC3339Nano)
	}
	if !clearedAt.IsZero() {
		fields.ClearedAt = clearedAt.Format(time.RFC3339Nano)
	}
	explain, err := json.Marshal(fields)
	if err != nil {
		return
	}
	_, _ = w.audit.Append(ctx, audit.Event{Kind: KindSecretsClipboardWrite, Action: clipboardActionClass, Explain: explain})
}

// RearmPendingClears re-arms every persisted pending clear on daemon
// start (R-21.242): a row already past ClearDueAt fires immediately; a
// row not yet due is re-scheduled for its remaining time. Exported and
// ready to be wired; this ticket's lane forbids touching internal/daemon
// (another agent holds it concurrently), so no caller installs it on the
// daemon start path here — see internal/build/testonly-allow.json.
func RearmPendingClears(ctx context.Context, w ClipboardWriter, clock runtime.Clock, scheduler ClipboardScheduler, store PendingClearStore) error {
	cw, ok := w.(*clipboardWriter)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "secrets: RearmPendingClears requires the production ClipboardWriter")
	}
	pending, err := store.List(ctx)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not list pending clipboard clears")
	}
	now := clock.Now()
	for _, pc := range pending {
		ref := pc.RefID
		if !now.Before(pc.ClearDueAt) {
			cw.fireClear(ctx, ref)
			continue
		}
		remaining := pc.ClearDueAt.Sub(now)
		scheduler.AfterFunc(remaining, func() { cw.fireClear(context.Background(), ref) })
	}
	return nil
}
