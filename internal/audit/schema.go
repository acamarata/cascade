package audit

// Purpose: the audit domain's on-record schema, the CLOSED eleven-value
//   event-kind enum, the caller-supplied Event, the sealed Record that is
//   written, the key layout inside the audit domain namespace, and the
//   hash chain that makes a later alteration detectable rather than
//   silent.
// Inputs: caller-supplied Event fields; stored record bytes on the way
//   back in.
// Outputs: encoded record bytes, a record's content hash, or a
//   pkg/cascade taxonomy error.
// Constraints: pure functions only, no clock, no I/O, no randomness
//   beyond newID's crypto/rand draw. Validation FAILS CLOSED: an event
//   kind outside the eleven, a non-JSON explain body, or a control
//   character in a field is refused, never stored "best effort".
//
//   CONTRACT DEVIATION (recorded, not papered over). The contract for
//   this ticket says the audit schema is "the audit domain migration
//   using the B/S-02.T3 DSL ... registered in internal/audit/schema.go".
//   The tree has moved past that: every cascade.db domain persists
//   through pkg/provider.Store, whose SQLite driver keeps one physical
//   kv table (providers/sqlite/driver.go schemaDDL), and the production
//   migration set is deliberately empty (cmd/cascade/daemon_unix_store.go:
//   "adding speculative steps with nothing to migrate would be its own
//   Article-1 violation"). A CREATE TABLE audit_events step here would
//   emit a table no code in this package ever reads. This file therefore
//   defines the record schema that is really enforced, and the domain's
//   anchor table stays storage.Bootstrap's, as it is for every other
//   domain. See the journal for both sides quoted.
// SPORT: internal.audit.Record/ADDED (P1-E09-W2-S18-T2).

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Kind is the closed set of auditable event kinds. It is a defined string
// type so a typo is a compile-time mismatch rather than a row nothing can
// ever query for. The set is CLOSED at the eleven values below: a
// consumer needing a twelfth amends this package's contract instead of
// minting one at a call site, which is why Append refuses an unknown kind
// outright.
type Kind string

// The eleven ratified event kinds.
const (
	KindPolicyDecide     Kind = "policy.decide"
	KindPolicyRoute      Kind = "policy.route"
	KindApprovalEnqueue  Kind = "approval.enqueue"
	KindApprovalDedup    Kind = "approval.dedup"
	KindApprovalExpire   Kind = "approval.expire"
	KindConfigReload     Kind = "config.reload"
	KindApprovalGrant    Kind = "approval.grant"
	KindApprovalDeny     Kind = "approval.deny"
	KindElevationAttempt Kind = "elevation.attempt"
	KindElevationGrant   Kind = "elevation.grant"
	KindElevationDeny    Kind = "elevation.deny"
)

// AllKinds is the enum in a stable, documented order. Ranging over this
// slice (never a map) keeps every derived listing deterministic.
var AllKinds = []Kind{
	KindPolicyDecide, KindPolicyRoute,
	KindApprovalEnqueue, KindApprovalDedup, KindApprovalExpire,
	KindConfigReload,
	KindApprovalGrant, KindApprovalDeny,
	KindElevationAttempt, KindElevationGrant, KindElevationDeny,
}

// validKinds is the membership set Valid consults, built once from
// AllKinds so the two can never disagree.
var validKinds = func() map[Kind]bool {
	m := make(map[Kind]bool, len(AllKinds))
	for _, k := range AllKinds {
		m[k] = true
	}
	return m
}()

// Valid reports whether k is one of the eleven ratified kinds. Anything
// else, including the empty Kind, is invalid.
func (k Kind) Valid() bool { return validKinds[k] }

// Domain sentinels. Each names one refusal precisely and wraps exactly one
// Kind from pkg/cascade's frozen fourteen; none is invented here.
var (
	// ErrUnknownKind is returned for an event kind outside the eleven.
	ErrUnknownKind = cascade.New(cascade.KindInvalidInput, "audit: unknown event kind")
	// ErrInvalidEvent is returned for an otherwise malformed event.
	ErrInvalidEvent = cascade.New(cascade.KindInvalidInput, "audit: invalid event")
	// ErrInvalidFilter is returned for a query filter that cannot be
	// understood. It exists so an unparseable filter refuses rather than
	// widening into "match everything", which would disclose records the
	// caller never asked to see.
	ErrInvalidFilter = cascade.New(cascade.KindInvalidInput, "audit: invalid query filter")
	// ErrNoSuchRecord is returned when no record carries the given id.
	ErrNoSuchRecord = cascade.New(cascade.KindNotFound, "audit: no such audit record")
	// ErrTampered is returned when a stored record does not match its own
	// content hash, when the chain between two consecutive records is
	// broken, or when records are missing from the sequence. It is the
	// append-only guarantee's alarm: reads fail closed on it rather than
	// returning a record that may have been rewritten underneath the API.
	ErrTampered = cascade.New(cascade.KindIntegrity, "audit: audit log integrity check failed")
	// ErrAlreadyRecorded is returned when an append would land on a
	// sequence number that already holds a record. No write path in this
	// package overwrites one; this is what that promise looks like when
	// two writers race.
	ErrAlreadyRecorded = cascade.New(cascade.KindConflict, "audit: sequence number already holds a record")
	// ErrStoreUnavailable is returned when the backing store fails.
	ErrStoreUnavailable = cascade.New(cascade.KindUnavailable, "audit: audit store unavailable")
)

// namespace is the pkg/provider.Store scoping argument every key in this
// package is written under: the ratified audit domain from R-14.5's closed
// ten, taken from internal/storage rather than re-spelled as a literal.
const namespace = string(storage.DomainAudit)

// Key prefixes inside the audit namespace. recordPrefix is scanned in key
// order, which is sequence order, which is insertion order. None of these
// collides with internal/events' own "event:" and "cursor:" prefixes,
// which share this namespace when the bus persists through the same store.
const (
	recordPrefix = "rec:"
	indexPrefix  = "idx:"
	headKey      = "head"
)

// seqDigits zero-pads a sequence number wide enough that lexical key order
// equals numeric order for every uint64 (max uint64 has 20 digits), which
// is what lets Store.Scan's key-order walk double as oldest-first order.
const seqDigits = 20

func recordKey(seq uint64) string { return fmt.Sprintf("%s%0*d", recordPrefix, seqDigits, seq) }
func indexKey(id string) string   { return indexPrefix + id }

// maxFieldBytes bounds every free-text record field. An audit record is an
// index into what happened, not a place to park a payload, and an
// unbounded field is how a caller accidentally copies the thing being
// audited into the record that audits it.
const maxFieldBytes = 512

// Event is what a caller hands Append: everything about an auditable
// action except the identity, ordering, and time the log itself assigns.
//
// There is deliberately no field for a secret value, a plaintext
// credential, or a raw parameter blob. ParamsHash is the sanctioned way to
// record "these were the parameters" without recording the parameters:
// hash them with HashParams and store the digest.
type Event struct {
	// Kind is one of the eleven ratified kinds.
	Kind Kind `json:"kind"`
	// Actor names who or what took the action (a user id, a plugin id,
	// "scheduler").
	Actor string `json:"actor"`
	// Action names what was attempted, in the vocabulary of the
	// subsystem that took it.
	Action string `json:"action"`
	// ParamsHash is a digest of the action's parameters. See HashParams.
	ParamsHash string `json:"params_hash,omitempty"`
	// RiskLevel is the risk classification as a plain string, carrying
	// the canonical String() value from the deciding subsystem. It is a
	// string, not an imported type, because the dependency direction is
	// policy to audit and never back.
	RiskLevel string `json:"risk_level,omitempty"`
	// Verdict is the decision as a plain string, on the same terms as
	// RiskLevel.
	Verdict string `json:"verdict,omitempty"`
	// Explain is the JSON-shaped rationale: profile name, overlays
	// applied, deny-list hit, override reason. Empty or a valid JSON
	// value; anything else is refused.
	Explain json.RawMessage `json:"explain,omitempty"`
	// PolicySnapshot is the resolved policy as it stood at decision time,
	// so Explain can reconstruct the decision later without depending on
	// what the policy says today. Empty or a valid JSON value.
	PolicySnapshot json.RawMessage `json:"policy_snapshot,omitempty"`
	// Outcome is what actually happened after the decision.
	Outcome string `json:"outcome,omitempty"`
}

// Record is one sealed entry in the log: an Event plus the identity,
// position, time, and chain linkage the log assigned when it was appended.
// Every field is written once and never rewritten.
type Record struct {
	// Seq is the record's 1-based position in the log. Sequence numbers
	// are gapless: a gap is missing records, which Query reports as
	// tampering rather than skipping over.
	Seq uint64 `json:"seq"`
	// ID is the record's ULID, stable for the life of the record.
	ID string `json:"id"`
	// TSUnixNano is the append instant, read from the injected clock.
	TSUnixNano int64 `json:"ts_unix_nano"`
	// Event is the caller-supplied body.
	Event
	// PrevHash is the Hash of the record at Seq-1, or empty for Seq 1.
	PrevHash string `json:"prev_hash,omitempty"`
	// Hash is the content hash of every other field of this record.
	Hash string `json:"hash"`
}

// Time returns the instant this record was appended.
func (r Record) Time() time.Time { return time.Unix(0, r.TSUnixNano).UTC() }

// HashParams returns the digest a caller stores in Event.ParamsHash. It is
// the one supported way to say "these were the parameters" in a record
// without putting the parameters in the record.
func HashParams(params []byte) string {
	sum := blake3.Sum256(params)
	return hex.EncodeToString(sum[:])
}
