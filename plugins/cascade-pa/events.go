package cascadepa

// Purpose: the review-queue weekly-digest nudge. G/S-14.T3's DigestJob
//
//	fires MemoryWeeklyDigestReady on an in-process Go sink inside the
//	daemon; there is no wire-level publish/subscribe RPC a separate
//	`cascade chat` process can attach to (verified against the tree: no
//	memory.review.subscribe or equivalent method exists, and adding one
//	would be a new RPC method this ticket's contract forbids). This file's
//	DigestSubscriber is therefore a PULL seam: it reads the same
//	memory.review.list RPC the review-chat handler already uses and
//	reports the live pending count as the digest's actionable content —
//	the observable behavior the acceptance criteria name (a nudge when
//	pending > 0, suppressed at 0) without inventing a fourth RPC or a
//	push transport the daemon does not offer. See the ticket journal for
//	the full contract-vs-tree note.
//
// Inputs: the DigestSubscriber seam a composition root injects.
// Outputs: CheckDigestNudge's (nudge, shown) pair.
// Constraints: plugins/** may import pkg/** only, never internal/**.
// SPORT: plugins/cascade-pa:review-chat (ADD) — P1-E22-W5-S47-T4;
//
//	event:MemoryWeeklyDigest subscriber (ADD).

import (
	"context"
	"fmt"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DigestSignal is what one digest check reports: how many candidates are
// currently pending review.
type DigestSignal struct {
	PendingCount int
}

// DigestSubscriber is the seam events.go calls through to learn whether a
// nudge is due. A composition root injects a real implementation over
// memory.review.list via SetDigestSubscriber.
type DigestSubscriber interface {
	// CheckDigest reports the current pending count.
	CheckDigest(ctx context.Context) (DigestSignal, error)
}

// errDigestUnconfigured is returned when no composition root has wired a
// DigestSubscriber. CheckDigestNudge treats this the same as any other
// subscriber error: silently no nudge, never a crash of the chat session
// over a missing convenience feature.
var errDigestUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat: no digest subscriber wired into this session")

type unconfiguredDigestSubscriber struct{}

func (unconfiguredDigestSubscriber) CheckDigest(context.Context) (DigestSignal, error) {
	return DigestSignal{}, errDigestUnconfigured
}

var digestSubscriberState struct {
	mu sync.RWMutex
	s  DigestSubscriber
}

// SetDigestSubscriber injects the real DigestSubscriber. Tests call it
// directly to inject a stub.
func SetDigestSubscriber(s DigestSubscriber) {
	digestSubscriberState.mu.Lock()
	digestSubscriberState.s = s
	digestSubscriberState.mu.Unlock()
}

func activeDigestSubscriber() DigestSubscriber {
	digestSubscriberState.mu.RLock()
	defer digestSubscriberState.mu.RUnlock()
	if digestSubscriberState.s == nil {
		return unconfiguredDigestSubscriber{}
	}
	return digestSubscriberState.s
}

// FormatDigestNudge renders sig as the proactive nudge line, or reports
// shown=false when there is nothing to nudge about. A zero pending count
// is suppressed rather than nudged — an empty queue is not news.
func FormatDigestNudge(sig DigestSignal) (nudge string, shown bool) {
	if sig.PendingCount <= 0 {
		return "", false
	}
	noun := "memory"
	if sig.PendingCount != 1 {
		noun = "memories"
	}
	return fmt.Sprintf("You have %d %s ready to review — type /memory review next to start",
		sig.PendingCount, noun), true
}

// CheckDigestNudge asks the injected DigestSubscriber for the current
// signal and formats it. err is a genuine subscriber failure (e.g. the
// daemon is unreachable); a caller showing a chat session should treat it
// as "no nudge this turn", not as a reason to fail the turn.
func CheckDigestNudge(ctx context.Context) (nudge string, shown bool, err error) {
	sig, err := activeDigestSubscriber().CheckDigest(ctx)
	if err != nil {
		return "", false, err
	}
	nudge, shown = FormatDigestNudge(sig)
	return nudge, shown, nil
}
