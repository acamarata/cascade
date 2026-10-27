// Package policy (sameturn_expiry.go): Purpose: the same-turn ledger's
//
//	authorization entry point that also reports when the window closes, so
//	the policy.authorize verb can tell the operator the exact deadline.
//
// Inputs: the subject, the exact action text, and the ledger's clock.
//
// Outputs: the nonce, the expiry the entry was stored with, or a typed
//
//	refusal (see Authorize for the order the refusals are checked in).
//
// Constraints: the expiry returned is the expiry stored, read under the
//
//	same lock, so the caller is never told a deadline the ledger does not
//	enforce.
//
// SPORT: internal/policy SameTurnLedger.AuthorizeWithExpiry/ADDED (P1-SEC-10).
package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AuthorizeWithExpiry records subject's authorization of action for this
// turn and returns the nonce identifying it and the instant its backstop
// window closes. It refuses exactly what Authorize refuses, in the same
// order.
func (l *SameTurnLedger) AuthorizeWithExpiry(
	ctx context.Context, subject Subject, action string,
) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, cascade.Wrapf(cascade.KindCanceled, err,
			"policy: same-turn authorization was canceled")
	}
	if err := subject.Validate(); err != nil {
		return "", time.Time{}, err
	}
	if action == "" {
		return "", time.Time{}, cascade.New(cascade.KindInvalidInput,
			"policy: a same-turn authorization must name the action it authorizes")
	}
	if rpc.IsElevated(action, nil) {
		return "", time.Time{}, newSameTurnElevation(action)
	}
	nonce, err := newNonce()
	if err != nil {
		return "", time.Time{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := sameTurnKey(subject, action)
	if entry, ok := l.entries[key]; ok && !l.expired(entry) {
		return "", time.Time{}, newSameTurnDuplicate(action)
	}
	exp := l.clock.Now().Add(sameTurnWindow)
	l.entries[key] = sameTurnEntry{nonce: nonce, exp: exp}
	return nonce, exp, nil
}
