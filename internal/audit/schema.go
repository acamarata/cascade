package audit

// Purpose: the audit domain's on-record schema, the CLOSED fifteen-value
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
//   kind outside the fifteen, a non-JSON explain body, or a control
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
// ever query for. The set is CLOSED at the fifteen values below: a
// consumer needing another amends this package's contract instead of
// minting one at a call site, which is why Append refuses an unknown kind
// outright.
type Kind string

// The ratified event kinds: R-21.235's fourteen plus effect.external.
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

	// R-21.235 ratifies fourteen, not eleven. I/S-18.T2 shipped eleven and
	// its journal recorded the enum as "closed 11-value", so the ruling's
	// remaining three were never added and every consumer of them failed
	// closed at Append. H/S-16.T4's clipboard audit is the first to need
	// one (R-14.200).
	KindSecretsClipboardWrite  Kind = "secrets.clipboard_write"
	KindSecretsQuarantineFlush Kind = "secrets.quarantine_flush"
	KindVaultAccess            Kind = "vault.access"

	// KindExternalEffect is the one effect kind (P1-SEC-33): the intent and
	// terminal records an EffectLog writes for an external effect whose site
	// has no domain kind of its own. It amends R-21.235's closed set once;
	// a site with a domain kind keeps using that kind for its effect records.
	KindExternalEffect Kind = "effect.external"
)

// AllKinds is the enum in a stable, documented order. Ranging over this
// slice (never a map) keeps every derived listing deterministic.
var AllKinds = []Kind{
	KindPolicyDecide, KindPolicyRoute,
	KindApprovalEnqueue, KindApprovalDedup, KindApprovalExpire,
	KindConfigReload,
	KindApprovalGrant, KindApprovalDeny,
	KindElevationAttempt, KindElevationGrant, KindElevationDeny,
	KindSecretsClipboardWrite, KindSecretsQuarantineFlush, KindVaultAccess,
	KindExternalEffect,
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

// Valid reports whether k is one of the fifteen ratified kinds. Anything
// else, including the empty Kind, is invalid.
func (k Kind) Valid() bool { return validKinds[k] }

// Domain sentinels. Each names one refusal precisely and wraps exactly one
// Kind from pkg/cascade's frozen fourteen; none is invented here.
var (
	// ErrUnknownKind is returned for an event kind outside the fifteen.
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
	// effectPrefix holds the effect index (effect.go): one row per effect
	// key, written in the same transaction as the record it describes.
	effectPrefix = "eff:"
)

// seqDigits zero-pads a sequence number wide enough that lexical key order
// equals numeric order for every uint64 (max uint64 has 20 digits), which
// is what lets Store.Scan's key-order walk double as oldest-first order.
const seqDigits = 20

func recordKey(seq uint64) string { return fmt.Sprintf("%s%0*d", recordPrefix, seqDigits, seq) }
func indexKey(id string) string   { return indexPrefix + id }
func effectKey(key string) string { return effectPrefix + key }

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
	// Kind is one of the fifteen ratified kinds.
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
	// EffectKey and EffectPhase mark an effect record (effect.go). Only
	// EffectLog sets them; Append refuses an Event that carries either.
	// They are declared last and omitted when empty, so every record
	// written before they existed encodes, and therefore hashes, exactly
	// as it did (testdata/pre-effect-log). Being ordinary fields, they are
	// sealed in the record hash like every other.
	EffectKey   string `json:"effect_key,omitempty"`
	EffectPhase string `json:"effect_phase,omitempty"`
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

// stamped records seq as the row's own write: the intent's sequence for an
// intent row, the terminal's for any other.
func (r effectRow) stamped(seq uint64) effectRow {
	if r.Phase == EffectIntent {
		r.IntentSeq = seq
	} else {
		r.TerminalSeq = seq
	}
	return r
}

// replayEffects rebuilds, from effect records in sequence order, the index
// rows their writes must have left, plus each key's intent record. A
// second intent for a key, or a terminal record with no open intent, is
// something no write path produces, so it is reported as tampering.
func replayEffects(recs []Record) (map[string]effectRow, map[string]Record, error) {
	rows, intents := map[string]effectRow{}, map[string]Record{}
	for _, rec := range recs {
		key, phase := rec.EffectKey, EffectPhase(rec.EffectPhase)
		cur, begun := rows[key]
		switch {
		case key == "":
			continue
		case phase == EffectIntent && !begun:
			rows[key], intents[key] = effectRow{Phase: EffectIntent, IntentSeq: rec.Seq}, rec
		case phase != EffectIntent && begun && cur.Phase == EffectIntent:
			rows[key] = effectRow{Phase: phase, IntentSeq: cur.IntentSeq, TerminalSeq: rec.Seq}
		default:
			return nil, nil, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
				"record %d (%s for %q) does not follow from the records before it", rec.Seq, phase, key)
		}
	}
	return rows, intents, nil
}
