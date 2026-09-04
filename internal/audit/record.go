package audit

// Purpose: the record-integrity half of the audit schema, the content
//   hash that seals a record, the verification every read runs before it
//   returns one, the fail-closed validation an Event must pass before it
//   is ever written, and ULID minting.
// Inputs: an Event from a caller; stored bytes from the store.
// Outputs: a sealed or verified Record, or a pkg/cascade taxonomy error.
// Constraints: pure functions, no I/O and no clock read (newID takes the
//   instant as an argument); crypto/rand only, never math/rand.
// SPORT: internal.audit.Record/ADDED (P1-E09-W2-S18-T2).

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// contentHash computes a record's hash over every field except Hash
// itself. Encoding is encoding/json over a struct, whose field order is
// its declaration order, so the digest is deterministic across runs and
// builds without a separate canonicalization pass.
func contentHash(r Record) (string, error) {
	r.Hash = ""
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "audit: hashing record")
	}
	sum := blake3.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// seal fills in a record's Hash. It is called once, by the append path.
func seal(r Record) (Record, error) {
	h, err := contentHash(r)
	if err != nil {
		return Record{}, err
	}
	r.Hash = h
	return r, nil
}

// verify recomputes a decoded record's hash and refuses it on a mismatch.
// This is what turns an edit made behind the API's back into a detected
// failure instead of an accepted answer.
func verify(r Record) error {
	want, err := contentHash(r)
	if err != nil {
		return err
	}
	if want != r.Hash {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"record %d (%s) does not match its content hash", r.Seq, r.ID)
	}
	return nil
}

// decodeRecord parses stored bytes and verifies the record's own hash
// before returning it. No caller in this package sees an unverified
// record: a damaged or rewritten entry is refused whole.
func decodeRecord(data []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"stored record is not decodable: %v", err)
	}
	if err := verify(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// validateEvent refuses anything that must not reach the log. It fails
// closed on every branch: an unknown kind, a missing actor or action, an
// over-long or control-character-bearing field, or a rationale body that
// is not JSON.
func validateEvent(e Event) error {
	if !e.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownKind, "kind %q", string(e.Kind))
	}
	fields := map[string]string{
		"actor": e.Actor, "action": e.Action, "params_hash": e.ParamsHash,
		"risk_level": e.RiskLevel, "verdict": e.Verdict, "outcome": e.Outcome,
	}
	for _, name := range []string{"actor", "action", "params_hash", "risk_level", "verdict", "outcome"} {
		if err := validateField(name, fields[name]); err != nil {
			return err
		}
	}
	if e.Actor == "" || e.Action == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent,
			"actor and action are both required")
	}
	if err := validateJSON("explain", e.Explain); err != nil {
		return err
	}
	return validateJSON("policy_snapshot", e.PolicySnapshot)
}

// validateField bounds one free-text field and refuses control characters,
// which would otherwise let a record forge line structure in any surface
// that renders it.
func validateField(name, value string) error {
	if len(value) > maxFieldBytes {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent,
			"%s is %d bytes, over the %d-byte limit", name, len(value), maxFieldBytes)
	}
	if i := strings.IndexFunc(value, isControl); i >= 0 {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent,
			"%s contains a control character at offset %d", name, i)
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// validateJSON refuses a rationale body that is present but not JSON. A
// body that cannot be parsed cannot be explained later, and storing it
// anyway would put a record in the log that Explain must fail on.
func validateJSON(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if !json.Valid(raw) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent, "%s is not valid JSON", name)
	}
	return nil
}

// crockford is the ULID alphabet (Crockford base32: no I, L, O, or U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newID returns a ULID for the given instant: 48 bits of millisecond
// timestamp followed by 80 bits of randomness, rendered as 26 Crockford
// base32 characters. Ordering never depends on it, the log's sequence
// number is what orders records, so two records minted in the same clock
// tick still have one defined order.
func newID(now time.Time) (string, error) {
	var raw [16]byte
	ms := uint64(now.UTC().UnixMilli())
	var be [8]byte
	binary.BigEndian.PutUint64(be[:], ms)
	copy(raw[0:6], be[2:8])
	if _, err := cryptorand.Read(raw[6:]); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "audit: generating record id")
	}
	out := make([]byte, 26)
	var acc, bits uint32
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint32(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&0x1f]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	out[pos] = crockford[acc&0x1f]
	return string(out), nil
}
