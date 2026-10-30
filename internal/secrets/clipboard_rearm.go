// Purpose: the durable clipboard clear and its daemon re-arm. A clear is an
//
//	effect record (intent, clearValue, confirm) and the pending row is
//	deleted only after a confirmed clear; a failed clear marks the intent
//	unknown-outcome and keeps the row. RearmHostPendingClears runs at daemon
//	start: a due row clears now, a not-yet-due row is scheduled for the
//	remainder, a failed scheduled clear is retried, and every clipboard
//	intent a crash left open is marked unknown-outcome.
//
// Inputs: the injected clock, the daemon's effect log, the pending store,
//
//	a scheduler and the host clipboard backend.
//
// Outputs: RearmHostPendingClears.
//
// Constraints: a clear runs even when its intent could not be recorded
//
//	(the secret leaving the clipboard outranks the record), but then the row
//	is kept so the re-arm repeats it under a record. Nothing here reads or
//	holds a payload.
//
// SPORT: internal/secrets RearmHostPendingClears/ADDED,
//
//	RearmPendingClears/REMOVED (P1-SEC-09).

package secrets

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clearRetryInterval and clearRetries bound how a daemon retries a
// scheduled clear that failed; past them the row waits for the next start.
const (
	clearRetryInterval = time.Minute
	clearRetries       = 3
)

// ClipboardScheduler abstracts the one-shot countdown so tests fire it
// deterministically.
type ClipboardScheduler interface {
	AfterFunc(d time.Duration, fn func()) (stop func())
}

type systemClipboardScheduler struct{}

// NewSystemClipboardScheduler returns the production scheduler, the one
// place time.AfterFunc appears.
func NewSystemClipboardScheduler() ClipboardScheduler { return systemClipboardScheduler{} }

func (systemClipboardScheduler) AfterFunc(d time.Duration, fn func()) (stop func()) {
	t := time.AfterFunc(d, fn)
	return func() { t.Stop() }
}

// clipboardClearer runs the durable clear. clipboardWriter embeds it; the
// re-arm builds one over the daemon's effect log.
type clipboardClearer struct {
	clock   runtime.Clock
	effects clipboardEffects
	store   PendingClearStore
	ops     clipboardOps
}

// clear runs one durable clear of ref and reports whether it was confirmed
// and its row removed. Any other outcome keeps the row.
func (c *clipboardClearer) clear(ctx context.Context, ref string) error {
	h, berr := c.beginClear(ctx, ref)
	begun := h.IntentSeq != 0
	if err := c.ops.clearValue(ctx); err != nil {
		var merr error
		if begun {
			_, merr = c.effects.MarkUnknownOutcome(ctx, h)
		}
		return withCleanup(cascade.Wrap(cascade.KindUnavailable, err,
			"secrets: the clipboard clear failed; the pending clear is kept for the daemon to retry"), berr, merr)
	}
	if !begun {
		return cascade.Wrap(cascade.KindUnavailable, berr,
			"secrets: the clipboard was cleared but the clear could not be audited; the pending clear is kept so it is repeated under a record")
	}
	if _, err := c.effects.ConfirmEffect(ctx, h, "cleared"); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err,
			"secrets: the clipboard was cleared but the clear could not be confirmed; the pending clear is kept")
	}
	if err := c.store.Delete(ctx, ref); err != nil {
		return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err,
			"secrets: the clipboard was cleared but its pending row could not be removed; the daemon clears again")
	}
	return nil
}

// beginClear commits the clear intent under a fresh per-attempt key, so a
// retry after a failed or unknown clear can be recorded too.
func (c *clipboardClearer) beginClear(ctx context.Context, ref string) (audit.EffectHandle, error) {
	attempt, err := cascade.NewID()
	if err != nil {
		return audit.EffectHandle{}, err
	}
	key := clipboardClearAction + ":" + ref + ":" + attempt.String()
	return c.effects.BeginEffect(ctx, effectRequest(clipboardClearAction, key, PendingClear{RefID: ref, Platform: c.ops.platform()}))
}

// clipboardRecovery is what the re-arm needs from the daemon's effect log:
// the clear's effect writes plus the open-intent listing.
type clipboardRecovery interface {
	clipboardEffects
	PendingEffects(ctx context.Context) ([]audit.EffectHandle, error)
}

// RearmHostPendingClears re-arms every persisted pending clear over this
// host's clipboard tool and settles the clipboard intents a crash left
// open. effects must be the daemon log's *audit.EffectLog (or anything with
// its PendingEffects and MarkUnknownOutcome).
func RearmHostPendingClears(ctx context.Context, clock runtime.Clock, effects audit.EffectWriter,
	store PendingClearStore, scheduler ClipboardScheduler) error {
	return rearmWithOps(ctx, clock, effects, store, scheduler, newClipboardOps)
}

func rearmWithOps(ctx context.Context, clock runtime.Clock, effects audit.EffectWriter, store PendingClearStore,
	scheduler ClipboardScheduler, newOps func() (clipboardOps, error)) error {
	fx, ok := effects.(clipboardRecovery)
	if !ok || ctx == nil || clock == nil || store == nil || scheduler == nil || newOps == nil {
		return cascade.New(cascade.KindInvalidInput,
			"secrets: the clipboard re-arm requires a lifetime context, a clock, a recovering effect log, a pending store and a scheduler")
	}
	rows, err := store.List(ctx)
	if err != nil {
		return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "secrets: could not list pending clipboard clears")
	}
	if len(rows) > 0 {
		ops, err := newOps()
		if err != nil {
			return err
		}
		c := &clipboardClearer{clock: clock, effects: fx, store: store, ops: ops}
		now := clock.Now()
		for _, row := range rows {
			if now.Before(row.ClearDueAt) {
				c.schedule(ctx, scheduler, row.RefID, row.ClearDueAt.Sub(now), clearRetries)
				continue
			}
			// A due row clears now. A failure is already on record as
			// unknown-outcome with its row kept; it is retried rather than
			// refusing the daemon start over a clipboard tool.
			if c.clear(ctx, row.RefID) != nil {
				c.schedule(ctx, scheduler, row.RefID, clearRetryInterval, clearRetries)
			}
		}
	}
	return settleOpenIntents(ctx, fx)
}

// schedule arms one clear and, on failure, up to retries further attempts.
// A clear that still fails keeps its row for the next daemon start; its
// unknown-outcome record is the trail. ctx is the daemon's lifetime: once it
// ends the timer is stopped and a late fire does nothing, so no clear touches
// the effect log after the daemon closed it; the row waits for the next
// start.
func (c *clipboardClearer) schedule(ctx context.Context, s ClipboardScheduler, ref string, after time.Duration, retries int) {
	stop := s.AfterFunc(after, func() {
		if ctx.Err() != nil {
			return
		}
		if err := c.clear(ctx, ref); err != nil && retries > 0 && ctx.Err() == nil {
			c.schedule(ctx, s, ref, clearRetryInterval, retries-1)
		}
	})
	if stop != nil {
		context.AfterFunc(ctx, stop)
	}
}

// settleOpenIntents marks every clipboard intent with no terminal record
// unknown-outcome: the clipboard cannot be probed, so whether that effect
// ran is unknown. The rows (not these records) drive the clears.
func settleOpenIntents(ctx context.Context, fx clipboardRecovery) error {
	pending, err := fx.PendingEffects(ctx)
	if err != nil {
		return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "secrets: could not list open clipboard effect records")
	}
	for _, h := range pending {
		if !strings.HasPrefix(h.Key, clipboardWriteAction+":") && !strings.HasPrefix(h.Key, clipboardClearAction+":") {
			continue
		}
		if _, err := fx.MarkUnknownOutcome(ctx, h); err != nil {
			return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "secrets: could not settle an open clipboard effect record")
		}
	}
	return nil
}
