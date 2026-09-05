// Package policy (sameturn.go): Purpose: the same-turn authorization
//
//	ledger — the narrow, single-use window §5.15 reserves for an action the
//	deny-list otherwise refuses, and the always-refusing default an Engine
//	is built with.
//
// Inputs: the subject who typed the authorization, the exact action text
//
//	they authorized, and an injected Clock for expiry.
//
// Outputs: an unguessable nonce identifying the authorization, or a typed
//
//	refusal; and, at the seam, whether an action is authorized RIGHT NOW.
//
// Constraints: the window is as narrow as it can be made.
//
//	· It binds to ONE subject and ONE exact action string. Nothing is
//	  normalized here on purpose: normalizing would WIDEN the window, so
//	  that authorizing `rm -rf /` also covered `sh -c "rm -rf /"`. A
//	  different spelling is a different action and is not authorized.
//	· It is SINGLE USE. Authorized consumes the entry, so the same
//	  authorization cannot answer twice and cannot be replayed.
//	· It NEVER PERSISTS. The ledger is a map in daemon memory with no
//	  storage behind it, so a restart drops every authorization, and
//	  EndTurn drops them at the end of the turn that created them.
//	· It is NOT INHERITABLE. A later action in the same turn has its own
//	  key and finds nothing.
//	· It CANNOT STAND IN FOR AN ATTESTATION (R-21.231). Authorize refuses
//	  every §5.14 elevation-class verb outright, so no nonce for one can
//	  ever exist. The elevated-verb table is internal/rpc's canonical one,
//	  called with nil params exactly as the daemonless guard calls it;
//	  there is no second list here.
//
// SPORT: internal/policy SameTurnLedger/ADDED, NoSameTurnAuth/ADDED
//
//	(P1-E09-W2-S17-T4).
package policy

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The stable identifier strings for this file's refusals (R-14.152).
const (
	// CodeSameTurnDuplicate marks a second Authorize for an action that is
	// already authorized in this turn.
	CodeSameTurnDuplicate = "sameturn-duplicate"
	// CodeSameTurnElevation marks an elevation-class action, which a
	// same-turn authorization may never cover.
	CodeSameTurnElevation = "sameturn-elevation-required"
)

// ErrSameTurnDuplicate is the comparison target for a duplicate Authorize.
var ErrSameTurnDuplicate = &ClassifyError{
	Code:  CodeSameTurnDuplicate,
	Cause: cascade.New(cascade.KindConflict, CodeSameTurnDuplicate),
}

// ErrSameTurnElevation is the comparison target for R-21.231's refusal. It
// carries KindElevationRequired, so errors.Is(err,
// cascade.ErrElevationRequired) finds it and the caller is told to run the
// attestation flow rather than to re-type the authorization.
//
// The name is not ErrElevationRequired because this package already
// exports a FUNCTION of that name for the daemonless guard
// (daemonless_elevation.go); the contract's spelling is unavailable and a
// second symbol with the same meaning would be worse than a clear one.
var ErrSameTurnElevation = &ClassifyError{
	Code:  CodeSameTurnElevation,
	Cause: cascade.New(cascade.KindElevationRequired, CodeSameTurnElevation),
}

// sameTurnWindow is the backstop lifetime of an authorization. EndTurn is
// the primary expiry; this bounds an authorization whose turn never ended
// because the surface that opened it went away.
const sameTurnWindow = 2 * time.Minute

// nonceBytes is the entropy in an authorization nonce.
const nonceBytes = 16

// sameTurnEntry is one live authorization.
type sameTurnEntry struct {
	// nonce identifies this authorization in an audit row. It is
	// crypto-random, so two authorizations for the same action are
	// distinguishable and neither is guessable.
	nonce string
	// exp is when the backstop window closes.
	exp time.Time
}

// SameTurnLedger is the in-memory authorization ledger.
type SameTurnLedger struct {
	mu      sync.Mutex
	clock   Clock
	entries map[string]sameTurnEntry
}

var _ SameTurnAuthorizer = (*SameTurnLedger)(nil)

// NewSameTurnLedger builds the ledger over clock. The clock is required:
// a ledger that cannot tell the time could not close its own window.
func NewSameTurnLedger(clock Clock) (*SameTurnLedger, error) {
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"policy: same-turn ledger requires a clock")
	}
	return &SameTurnLedger{clock: clock, entries: map[string]sameTurnEntry{}}, nil
}

// sameTurnKey binds an authorization to one subject and one exact action.
// The NUL separator cannot occur in either component, so no pair of
// subject and action can collide with another pair.
func sameTurnKey(subject Subject, action string) string {
	return string(subject.Kind) + "/" + subject.ID + "\x00" + action
}

// Authorize records subject's authorization of action for this turn and
// returns the nonce identifying it.
//
// It refuses, in this order: a subject that names nobody, an empty action,
// an elevation-class action (R-21.231), and an action this subject has
// already authorized in this turn. The duplicate refusal exists so a
// second AUTHORIZE cannot quietly extend a window the first one opened.
func (l *SameTurnLedger) Authorize(
	ctx context.Context, subject Subject, action string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", cascade.Wrapf(cascade.KindCanceled, err,
			"policy: same-turn authorization was canceled")
	}
	if err := subject.Validate(); err != nil {
		return "", err
	}
	if action == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"policy: a same-turn authorization must name the action it authorizes")
	}
	if rpc.IsElevated(action, nil) {
		return "", newSameTurnElevation(action)
	}
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := sameTurnKey(subject, action)
	if entry, ok := l.entries[key]; ok && !l.expired(entry) {
		return "", newSameTurnDuplicate(action)
	}
	l.entries[key] = sameTurnEntry{nonce: nonce, exp: l.clock.Now().Add(sameTurnWindow)}
	return nonce, nil
}

// Authorized implements SameTurnAuthorizer, and CONSUMES the entry it
// finds. Consumption is what makes the authorization single-use: the
// evaluator asks this question once per evaluation, so a second evaluation
// of the same action in the same turn finds nothing and is refused.
//
// The seam has no separate Consume method to call — the tree froze
// SameTurnAuthorizer as this one question before this ticket landed — so
// the check and the consumption happen together under one lock, which is
// also the only shape that is atomic against a concurrent caller.
func (l *SameTurnLedger) Authorized(
	ctx context.Context, subject Subject, action string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, cascade.Wrapf(cascade.KindCanceled, err,
			"policy: the same-turn ledger was consulted after cancellation")
	}
	if subject.Validate() != nil || action == "" {
		return false, nil
	}
	key := sameTurnKey(subject, action)
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return false, nil
	}
	delete(l.entries, key)
	return !l.expired(entry), nil
}

// Consume drops the authorization for subject and action, whether or not
// one exists. It is idempotent, and it is how a surface abandons an
// authorization it opened and then did not spend.
func (l *SameTurnLedger) Consume(subject Subject, action string) {
	l.mu.Lock()
	delete(l.entries, sameTurnKey(subject, action))
	l.mu.Unlock()
}

// EndTurn drops every authorization. It is the primary expiry: a turn ends
// and nothing it authorized survives into the next one.
func (l *SameTurnLedger) EndTurn() {
	l.mu.Lock()
	l.entries = map[string]sameTurnEntry{}
	l.mu.Unlock()
}

// Live reports how many authorizations are outstanding, so a surface can
// tell an operator what a turn is still holding open.
func (l *SameTurnLedger) Live() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	live := 0
	for _, entry := range l.entries {
		if !l.expired(entry) {
			live++
		}
	}
	return live
}

// expired reports whether entry's backstop window has closed. The caller
// holds the lock.
func (l *SameTurnLedger) expired(entry sameTurnEntry) bool {
	return !l.clock.Now().Before(entry.exp)
}

// newNonce returns a crypto-random authorization identifier. A failure to
// read entropy refuses the authorization rather than falling back to a
// guessable value. crypto/rand is imported under the cryptorand alias the
// rest of the tree uses: Art.7.3 forbids unseeded math/rand in domain
// logic, and the alias states which package this actually is.
func newNonce() (string, error) {
	buf := make([]byte, nonceBytes)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", cascade.Wrapf(cascade.KindInternal, err,
			"policy: a same-turn authorization nonce could not be generated")
	}
	return hex.EncodeToString(buf), nil
}

// newSameTurnDuplicate builds the duplicate refusal.
func newSameTurnDuplicate(action string) *ClassifyError {
	return &ClassifyError{
		Code: ErrSameTurnDuplicate.Code,
		Cause: cascade.Newf(cascade.KindConflict,
			"policy: %s: %s is already authorized in this turn",
			CodeSameTurnDuplicate, quoteName(sanitize(action))),
	}
}

// newSameTurnElevation builds R-21.231's refusal.
func newSameTurnElevation(action string) *ClassifyError {
	return &ClassifyError{
		Code: ErrSameTurnElevation.Code,
		Cause: cascade.Newf(cascade.KindElevationRequired,
			"policy: %s: %s is elevation-class, so it needs a fresh attestation and "+
				"cannot be authorized in-turn", CodeSameTurnElevation, quoteName(sanitize(action))),
	}
}

// noSameTurn is the complete default (Art.1): nothing is ever same-turn
// authorized. An Engine is built with it, so an engine nobody has wired an
// authorizer into refuses every deny-list entry rather than depending on a
// nil check somewhere to mean the same thing.
type noSameTurn struct{}

// NoSameTurnAuth returns the always-refusing authorizer.
func NoSameTurnAuth() SameTurnAuthorizer { return noSameTurn{} }

// Authorized implements SameTurnAuthorizer: never.
func (noSameTurn) Authorized(context.Context, Subject, string) (bool, error) {
	return false, nil
}
