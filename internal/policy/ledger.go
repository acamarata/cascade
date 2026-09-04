// Package policy (ledger.go): Purpose: the single-use approval ledger —
//
//	the durable record of every approval nonce that has been redeemed, so a
//	replayed token is refused REGARDLESS of whether it has expired and
//	regardless of whether the daemon has restarted since.
//
// Inputs: a provider.Store (the B-layer abstraction; writes land in the
//
//	`audit` domain, whose per-domain fairness slot in the sqlite driver's
//	write executor serializes them), and a LedgerRecord per redemption.
//
// Outputs: a durable nonce row, or a refusal. There is no "maybe" answer:
//
//	Consume either records the nonce for the first time and returns nil, or
//	it returns an error and the caller must not act.
//
// Constraints: the write is a CONDITIONAL CREATE, not a read-then-write.
//
//	provider.Tx.CompareAndSwap with a nil old value writes only when the
//	key does not already exist and reports KindConflict when it does, which
//	makes "has this nonce been used" and "claim this nonce" one atomic step;
//	a Get-then-Put would let two concurrent redemptions of the same token
//	both observe an absent key and both proceed. A stored row that cannot
//	be decoded is STILL a used nonce: the ledger never re-opens a nonce
//	because its record was damaged, because that is precisely the state an
//	attacker would manufacture. Every non-conflict store failure is
//	returned unchanged and denies, so an unreachable store refuses
//	redemptions rather than allowing them.
//
// SPORT: internal/policy Ledger/ADDED, LedgerRecord/ADDED
//
//	(P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ledgerKeyPrefix namespaces consumed-nonce rows inside the `audit`
// domain, so they cannot collide with the audit log's own record and index
// key spaces (internal/audit owns "record/" and "index/").
const ledgerKeyPrefix = "approval/nonce/"

// maxNonceLen bounds a nonce. A nonce is an identifier, not a payload; an
// unbounded one is a storage-key hazard rather than a legitimate token.
const maxNonceLen = 128

// LedgerRecord is one consumed-nonce row. It deliberately carries no
// parameters and no action text: the ledger answers "has this nonce been
// spent", and the audit log next door is where the story of the action
// belongs.
type LedgerRecord struct {
	// Nonce is the token's single-use value. It is the row's identity.
	Nonce string `json:"nonce"`
	// RequestID is the approval request the nonce was minted for.
	RequestID string `json:"request_id"`
	// ActionHash is the digest of the action the approval was bound to,
	// so a forensic reader can tell WHICH action a spent nonce covered
	// without the action itself being stored.
	ActionHash string `json:"action_hash"`
	// Subject is the principal that redeemed it, rendered "kind:id".
	Subject string `json:"subject"`
	// ConsumedAtUnixNano is the redemption instant, read from the
	// injected clock.
	ConsumedAtUnixNano int64 `json:"consumed_at_unix_nano"`
}

// Validate refuses a row that could not identify a nonce. Both identity
// fields are required: a row with no nonce claims nothing, and a row with
// no request id cannot be traced back to what was approved.
func (r LedgerRecord) Validate() error {
	if err := validateNonce(r.Nonce); err != nil {
		return err
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return cascade.New(cascade.KindInvalidInput,
			"policy: ledger record has no request id")
	}
	return nil
}

// validateNonce enforces the nonce grammar. It runs on every write and on
// every key build, so a nonce that could not have been written can also
// never address a stored row.
func validateNonce(nonce string) error {
	if nonce == "" {
		return cascade.New(cascade.KindInvalidInput, "policy: ledger: empty nonce")
	}
	if len(nonce) > maxNonceLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: ledger: nonce is %d bytes, over the %d-byte limit",
			len(nonce), maxNonceLen)
	}
	if !validSubjectID(nonce) {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: ledger: %q is not a well-formed nonce", sanitize(nonce))
	}
	return nil
}

// ledgerKey builds the storage key for a nonce, after validating it.
func ledgerKey(nonce string) (string, error) {
	if err := validateNonce(nonce); err != nil {
		return "", err
	}
	return ledgerKeyPrefix + nonce, nil
}

// Ledger is the durable single-use nonce ledger. It is a real
// implementation over the B-layer store (Art.1), holds no cache, and keeps
// no in-memory set of spent nonces: the store is the only authority, so a
// restart cannot forget a spent token.
type Ledger struct {
	store provider.Store
	clock Clock
}

// NewLedger builds a Ledger over store. Both arguments are required: a
// ledger with no store could not remember a redemption, and one with no
// clock could not stamp when a redemption happened — and a ledger that
// cannot do either has no business answering a replay question.
func NewLedger(store provider.Store, clock Clock) (*Ledger, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: ledger requires a store")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: ledger requires a clock")
	}
	return &Ledger{store: store, clock: clock}, nil
}

// namespace is the `audit` storage domain, read from storage's own
// constant so the two can never drift.
func (l *Ledger) namespace() string { return string(storage.DomainAudit) }

// Consume claims rec.Nonce for the first and only time.
//
// It returns nil exactly once per nonce, for the life of the store. Every
// later call with the same nonce returns ErrTokenReplayed, whether the
// first call was seconds or months ago, whether the token has since
// expired, and whether the daemon restarted in between. A store that
// cannot be reached returns its own error, which denies.
func (l *Ledger) Consume(ctx context.Context, rec LedgerRecord) error {
	if l == nil {
		return cascade.New(cascade.KindInvalidInput, "policy: nil ledger")
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	key, err := ledgerKey(rec.Nonce)
	if err != nil {
		return err
	}
	rec.ConsumedAtUnixNano = l.clock.Now().UTC().UnixNano()
	encoded, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err,
			"policy: ledger: encoding the consumed-nonce record")
	}
	txErr := l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, l.namespace(), key, nil, encoded)
	})
	if txErr == nil {
		return nil
	}
	if errors.Is(txErr, cascade.ErrConflict) {
		return refuse(ErrTokenReplayed,
			"nonce for request %s has already been redeemed", sanitize(rec.RequestID))
	}
	return txErr
}
