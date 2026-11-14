package sync

import (
	"context"
	"errors"
)

// Purpose (this file): peer-journal join types and the Store interface.
// Inputs: JoinRequest from a peer.
// Outputs: JoinResult or a sentinel error for refusal cases.
// Constraints: no implementation yet; only types, constants, and interface.

// JoinProtocolVersion is the minimum protocol version a peer must support
// to participate in a join. Peers below this version are refused before
// any mutation occurs.
const JoinProtocolVersion = 1

// Sentinel errors for join refusal cases. Each maps to a cascade.Kind when
// crossing the wire per A-core-lower C-ERR.
var (
	// ErrJoinVersionTooLow indicates the peer's protocol version is below
	// JoinProtocolVersion.
	ErrJoinVersionTooLow = errors.New("sync: peer protocol version too low")

	// ErrJoinDuplicate indicates a (PeerID, PeerSeq) pair was already
	// recorded with different bytes.
	ErrJoinDuplicate = errors.New("sync: duplicate peer entry with different bytes")

	// ErrJoinMalformed indicates the peer entry failed validation.
	ErrJoinMalformed = errors.New("sync: malformed peer entry")

	// ErrJoinOverBound indicates the input exceeds allowed limits.
	ErrJoinOverBound = errors.New("sync: join input over bound")
)

// JournalState represents the persisted state of a peer journal.
type JournalState struct {
	// Entries is the ordered set of peer entries in the journal.
	Entries []PeerEntry
	// Version is the journal format version.
	Version int
}

// PeerEntry is a single record within a peer journal.
type PeerEntry struct {
	// PeerID identifies the originating peer.
	PeerID string
	// PeerSeq is the sequence number assigned by the peer.
	PeerSeq uint64
	// Data holds the serialized entry bytes.
	Data []byte
}

// JoinRequest is the input to a Store.Join call.
type JoinRequest struct {
	// ProtocolVersion is the peer's join protocol version.
	ProtocolVersion int
	// Entries are the peer entries to join.
	Entries []PeerEntry
}

// JoinResult is the output of a successful Store.Join call.
type JoinResult struct {
	// Accepted is the number of entries that were applied.
	Accepted int
	// Refused is the number of entries that were rejected.
	Refused int
}

// Store is the interface for executing a peer-journal join inside one
// store transaction. Implementations must refuse peers below the join
// protocol version, duplicate (PeerID, PeerSeq) pairs with different bytes,
// malformed entries, and over-bound input before any mutation occurs.
type Store interface {
	// Join executes the peer-journal join described by req inside one
	// store transaction. It returns ErrJoinVersionTooLow, ErrJoinDuplicate,
	// ErrJoinMalformed, or ErrJoinOverBound on refusal.
	Join(ctx context.Context, req JoinRequest) (JoinResult, error)
}
