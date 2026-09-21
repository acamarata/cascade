// Purpose: AwayController's collaborator-facing halves, split out of away.go
//
//	to stay under Art.10.3's 300-line file cap: the presence and
//	admission readings, the stall-notice drain that reclassifies
//	accumulated notifications, the return-digest hand-off, and the
//	M/S-27.T1 journal append both transition edges share.
//
// Inputs/Outputs/Constraints: see away.go's header — this file adds no
//
//	behavior of its own beyond what that doc comment already describes.
//	compileDigest is called from outside every critical section;
//	presenceActivity and admissionBusy are pure reads the caller may hold
//	c.mu across; drainStalls and appendJournal take or require c.mu
//	deliberately, so a stall notice cannot be lost to a concurrent return
//	and an episode's KindIntent can never be ordered after its own KindAck.
//
// SPORT: internal.notify.AwayController/ADDED (P1-E23-W5-S49-T2).

package notify

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
)

// compileDigest compiles and delivers episodeID's return digest from the
// items drained inside the return transition. A "" id means no episode
// closed on this edge (drained is nil then) and is a no-op. A nil Digest
// cannot compile anything, but the drain already happened, so the snapshot
// is returned to the normal queues rather than discarded: the
// no-destruction rule holds whether or not a compiler is wired.
func (c *AwayController) compileDigest(ctx context.Context, episodeID string, drained []Notification) {
	if episodeID == "" {
		return
	}
	if c.deps.Digest == nil {
		for _, n := range drained {
			c.deps.Router.requeue(n)
		}
		return
	}
	if err := c.deps.Digest.CompileDrained(ctx, episodeID, c.deps.EntityID, drained); err != nil {
		c.log.Error("notify: return-digest compile/dispatch failed", "episode", episodeID, "error", err)
	}
}

// drainStalls pulls every pending StallNotice and reclassifies the matching
// accumulated notifications to Urgent. It consumes the source ONLY while
// Away, inside c.mu, which is what makes AC#3 ("reclassified while Away
// appears as Urgent in the compiled digest") true rather than best-effort:
//
//   - The source's own contract delivers each notice exactly once, so
//     draining it while Active would DESTROY a notice this controller has
//     no accumulation buffer to apply it to. Reading the state first leaves
//     such a notice pending for the next away episode, where it is either
//     matched or reported as matching nothing. AC#3 obliges reclassification
//     only while Away, so not consuming is the honest half of that contract;
//     a notice still pending when its notification has long been dispatched
//     is WARNed there, never silently counted as handled.
//   - Consuming and applying inside the return transition's own lock means a
//     concurrent return cannot land between the check and the reclassify and
//     drop the notice with only a WARN.
//
// Like presenceActivity, the source is therefore called under c.mu: a
// re-entrant StallSource that called back into the controller would
// deadlock rather than corrupt the buffer.
func (c *AwayController) drainStalls() {
	if c.deps.Stalls == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateAway {
		return
	}
	for _, sn := range c.deps.Stalls.DrainStallNotices() {
		if n := c.reclassify(sn); n == 0 {
			c.log.Warn("notify: stall notice matched no accumulated notification",
				"notification", sn.NotificationID, "correlation", sn.CorrelationID)
		}
	}
}

// reclassify bumps every accumulated notification matching sn to Urgent and
// returns how many it changed.
func (c *AwayController) reclassify(sn StallNotice) int {
	n := c.deps.Router.reclassifyAccumulated(sn.NotificationID)
	if sn.CorrelationID != "" && sn.CorrelationID != sn.NotificationID {
		n += c.deps.Router.reclassifyAccumulated(sn.CorrelationID)
	}
	return n
}

// admissionBusy reports the machine side as busy, treating a nil source as
// permanently busy (fail closed: a controller with no admission signal
// never confirms Away on its own).
func (c *AwayController) admissionBusy() bool {
	if c.deps.Admission == nil {
		return true
	}
	return c.deps.Admission.Inflight() != 0 || c.deps.Admission.QueueDepth() != 0
}

// presenceActivity reads the injected presence source, zero Time when none
// is wired (no news, never "active").
func (c *AwayController) presenceActivity() time.Time {
	if c.deps.Presence == nil {
		return time.Time{}
	}
	return c.deps.Presence.LastActivity()
}

// appendJournal is the shared M/S-27.T1 append call. A nil journal.Store is
// a no-op, never a panic.
func (c *AwayController) appendJournal(ctx context.Context, kind journal.Kind, operationID string, p awayJournalPayload) {
	if c.deps.Journal == nil {
		return
	}
	body, err := json.Marshal(p)
	if err != nil {
		c.log.Error("notify: away journal payload marshal failed", "episode", operationID, "error", err)
		return
	}
	if _, err := c.deps.Journal.Append(ctx, c.deps.EntityID, kind, operationID, json.RawMessage(body)); err != nil {
		c.log.Error("notify: away journal append failed", "episode", operationID, "kind", kind.String(), "error", err)
	}
}
