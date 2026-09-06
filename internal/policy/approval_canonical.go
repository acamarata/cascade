// Package policy (approval_canonical.go): Purpose: the R-21.209 approval
//
//	record and its ONE encoding. A signature is only as strong as the
//	agreement between the two encoders that produce the bytes it covers, so
//	this file is the single canonical form: sorted keys, no insignificant
//	whitespace, one fixed number form, and a decoder that refuses anything
//	that is not byte-identical to what the encoder would have written.
//
// Inputs: an ApprovalRecord to encode, or untrusted bytes to decode.
// Outputs: ApprovalRecord, ApprovalTarget, CanonicalEncode, canonicalDecode,
//
//	ErrNonCanonical, ErrUnknownField, ErrDuplicateKey.
//
// Constraints: FAIL CLOSED. Bytes this decoder cannot fully account for are
//
//	REFUSED, never partially accepted: an unknown field, a repeated key,
//	trailing content, a different key order or any spacing difference all
//	refuse BEFORE any signature is examined.
//
// SPORT: internal/policy ApprovalRecord/ADDED, ApprovalTarget/ADDED,
//
//	CanonicalEncode/ADDED (P1-E08-W2-S16-T3).
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalSchemaVersion is the version of the signed record shape. It is
// part of the signed payload, so a future shape cannot be replayed as this
// one and this one cannot be replayed as a future one.
const ApprovalSchemaVersion = 1

// The stable identifier strings for this file's refusals, on the R-14.152
// pattern the classifier already uses: the taxonomy is frozen at fourteen
// kinds, so the specific reason survives as a stable string.
const (
	// CodeNonCanonical marks bytes that decode but are not the canonical
	// encoding of what they decode to.
	CodeNonCanonical = "approval-non-canonical"
	// CodeUnknownField marks a record carrying a field the shape does not
	// define.
	CodeUnknownField = "approval-unknown-field"
	// CodeDuplicateKey marks an object with the same key twice.
	CodeDuplicateKey = "approval-duplicate-key"
)

// The comparison targets for this file's refusals.
var (
	// ErrNonCanonical is returned for a non-canonical encoding.
	ErrNonCanonical = errors.New(CodeNonCanonical)
	// ErrUnknownField is returned for a field the shape does not define.
	ErrUnknownField = errors.New(CodeUnknownField)
	// ErrDuplicateKey is returned for a repeated object key.
	ErrDuplicateKey = errors.New(CodeDuplicateKey)
)

// ApprovalTarget names what an approval is bound to. All three components
// are part of the signed payload: an approval for one session's task is
// not an approval for another's.
type ApprovalTarget struct {
	// Node is the node the action runs on.
	Node string `json:"node"`
	// Session is the session the action belongs to.
	Session string `json:"session"`
	// Task is the task the action belongs to.
	Task string `json:"task"`
}

// ApprovalRecord is the R-21.209 signed payload: the complete set of facts
// a signature binds together. It signs the VERB as well as the parameter
// digest, because equal parameters under different verbs would otherwise
// let one approval be replayed as another.
type ApprovalRecord struct {
	// SchemaVersion is ApprovalSchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// KeyID names the signing key, so a rotation is detectable.
	KeyID string `json:"key_id"`
	// RequestID is the queued action's identifier.
	RequestID cascade.ID `json:"request_id"`
	// Verb is the RPC method name the approval authorizes.
	Verb string `json:"verb"`
	// ParamsDigest is the canonical-JSON digest of the parameters.
	ParamsDigest string `json:"params_digest"`
	// Requester is the principal that asked.
	Requester string `json:"requester"`
	// Approver is the principal that approved.
	Approver string `json:"approver"`
	// Origin names where the action entered from (cli, rpc, plugin,
	// bridge, node, conductor, hook).
	Origin string `json:"origin"`
	// Scope narrows the approval, e.g. to one repository path.
	Scope string `json:"scope"`
	// Target binds the approval to one node, session and task.
	Target ApprovalTarget `json:"target"`
	// Risk is the action's rung.
	Risk RiskLevel `json:"risk"`
	// Sensitivity is the data class of the material involved.
	Sensitivity DataClass `json:"sensitivity"`
	// PolicyVersion is the policy revision the decision was taken under.
	PolicyVersion string `json:"policy_version"`
	// Audience names who may verify this record.
	Audience string `json:"audience"`
	// Issued is the signing instant, from the injected clock.
	Issued time.Time `json:"issued"`
	// Expiry is the hard ceiling, never later than Issued+MaxApprovalTTL.
	Expiry time.Time `json:"expiry"`
	// Nonce is the single-use value the redemption ledger records.
	Nonce cascade.ID `json:"nonce"`
}

// CanonicalEncode renders r in the ONE canonical form: keys in sorted
// order, no insignificant whitespace, timestamps as RFC3339 nanoseconds in
// UTC, enums as their stable names, and the schema version as a plain
// integer. Encoding is total over valid records and is the definition the
// decoder checks against.
func CanonicalEncode(r ApprovalRecord) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	writeStringField(&b, "approver", r.Approver, true)
	writeStringField(&b, "audience", r.Audience, false)
	writeStringField(&b, "expiry", canonicalTime(r.Expiry), false)
	writeStringField(&b, "issued", canonicalTime(r.Issued), false)
	writeStringField(&b, "key_id", r.KeyID, false)
	writeStringField(&b, "nonce", r.Nonce.String(), false)
	writeStringField(&b, "origin", r.Origin, false)
	writeStringField(&b, "params_digest", r.ParamsDigest, false)
	writeStringField(&b, "policy_version", r.PolicyVersion, false)
	writeStringField(&b, "request_id", r.RequestID.String(), false)
	writeStringField(&b, "requester", r.Requester, false)
	writeStringField(&b, "risk", r.Risk.String(), false)
	b.WriteString(`,"schema_version":`)
	b.WriteString(strconv.Itoa(r.SchemaVersion))
	writeStringField(&b, "scope", r.Scope, false)
	writeStringField(&b, "sensitivity", r.Sensitivity.String(), false)
	b.WriteString(`,"target":{`)
	writeStringField(&b, "node", r.Target.Node, true)
	writeStringField(&b, "session", r.Target.Session, false)
	writeStringField(&b, "task", r.Target.Task, false)
	b.WriteByte('}')
	writeStringField(&b, "verb", r.Verb, false)
	b.WriteByte('}')
	return b.Bytes()
}

// writeStringField appends one "key":"value" pair, prefixed with a comma
// unless it is the first member of its object.
func writeStringField(b *bytes.Buffer, key, value string, first bool) {
	if !first {
		b.WriteByte(',')
	}
	b.WriteString(strconv.Quote(key))
	b.WriteByte(':')
	b.WriteString(strconv.Quote(value))
}

// canonicalTime renders an instant in the one time form: UTC, RFC3339 with
// nanosecond precision. A zero instant renders as the zero time rather
// than as an empty string, so a missing timestamp is still a timestamp the
// expiry check will reject.
func canonicalTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// canonicalWire mirrors ApprovalRecord for decoding, with the enums and
// timestamps as the strings the canonical form writes, so the decoder
// never has to guess what an unrecognised enum name meant.
type canonicalWire struct {
	Approver      string `json:"approver"`
	Audience      string `json:"audience"`
	Expiry        string `json:"expiry"`
	Issued        string `json:"issued"`
	KeyID         string `json:"key_id"`
	Nonce         string `json:"nonce"`
	Origin        string `json:"origin"`
	ParamsDigest  string `json:"params_digest"`
	PolicyVersion string `json:"policy_version"`
	RequestID     string `json:"request_id"`
	Requester     string `json:"requester"`
	Risk          string `json:"risk"`
	SchemaVersion int    `json:"schema_version"`
	Scope         string `json:"scope"`
	Sensitivity   string `json:"sensitivity"`
	Target        struct {
		Node    string `json:"node"`
		Session string `json:"session"`
		Task    string `json:"task"`
	} `json:"target"`
	Verb string `json:"verb"`
}

// canonicalDecode turns untrusted bytes into a record, refusing anything
// that is not exactly the canonical encoding of that record. The order of
// the checks is the security property: duplicate keys first (a decoder
// that silently keeps the last value would let two readers of the same
// bytes disagree), then unknown fields, then a byte-for-byte re-encode
// comparison, which is what makes "canonical" a decidable question rather
// than a list of formatting rules to remember.
func canonicalDecode(raw []byte) (ApprovalRecord, error) {
	if err := rejectDuplicateKeys(raw); err != nil {
		return ApprovalRecord{}, err
	}
	var wire canonicalWire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrUnknownField, "policy: approval record is not a well-formed record")
	}
	if dec.More() {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record carries trailing content")
	}
	rec, err := recordFromWire(wire)
	if err != nil {
		return ApprovalRecord{}, err
	}
	if !bytes.Equal(CanonicalEncode(rec), raw) {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record is not in canonical form")
	}
	return rec, nil
}

// recordFromWire converts the decoded strings into typed values. A
// timestamp that does not parse and an enum name that names no member both
// refuse rather than defaulting.
func recordFromWire(w canonicalWire) (ApprovalRecord, error) {
	issued, err := time.Parse(time.RFC3339Nano, w.Issued)
	if err != nil {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record has an unreadable issued time")
	}
	expiry, err := time.Parse(time.RFC3339Nano, w.Expiry)
	if err != nil {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record has an unreadable expiry time")
	}
	risk, ok := riskFromName(w.Risk)
	if !ok {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record names no known risk rung")
	}
	sens, ok := dataClassFromName(w.Sensitivity)
	if !ok {
		return ApprovalRecord{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrNonCanonical, "policy: approval record names no known sensitivity")
	}
	rec := ApprovalRecord{
		SchemaVersion: w.SchemaVersion, KeyID: w.KeyID, Verb: w.Verb,
		ParamsDigest: w.ParamsDigest, Requester: w.Requester, Approver: w.Approver,
		Origin: w.Origin, Scope: w.Scope, Risk: risk, Sensitivity: sens,
		PolicyVersion: w.PolicyVersion, Audience: w.Audience,
		Issued: issued, Expiry: expiry,
		Target: ApprovalTarget{Node: w.Target.Node, Session: w.Target.Session, Task: w.Target.Task},
	}
	if rec.RequestID, err = cascade.ParseID(w.RequestID); err != nil {
		return ApprovalRecord{}, err
	}
	if rec.Nonce, err = cascade.ParseID(w.Nonce); err != nil {
		return ApprovalRecord{}, err
	}
	return rec, nil
}

// riskFromName maps a canonical rung name back to its value. It is the
// inverse of RiskLevel.String over the valid rungs only: "invalid-risk-level"
// and every other string report false rather than mapping to a rung.
func riskFromName(name string) (RiskLevel, bool) {
	for r := L0; r <= L4; r++ {
		if r.String() == name {
			return r, true
		}
	}
	return 0, false
}

// dataClassFromName maps a canonical class name back to its value, on the
// same terms as riskFromName.
func dataClassFromName(name string) (DataClass, bool) {
	for d := DataClassPublic; d <= DataClassSecret; d++ {
		if d.String() == name {
			return d, true
		}
	}
	return 0, false
}
