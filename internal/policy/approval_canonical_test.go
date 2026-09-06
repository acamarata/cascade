package policy

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// specRecordFields is the R-21.209 field list, transcribed from the ruling
// prose: "{schema_version, key_id, request_id, verb, params_digest,
// requester, approver, origin, scope, target {node, session, task}, risk,
// sensitivity, policy_version, audience, issued, expiry, nonce}" and no
// others. It is written out here as a LITERAL so the encoder is checked
// against the specification rather than against a copy of itself.
var specRecordFields = []string{
	"approver", "audience", "expiry", "issued", "key_id", "nonce", "origin",
	"params_digest", "policy_version", "request_id", "requester", "risk",
	"schema_version", "scope", "sensitivity", "target", "verb",
}

// specTargetFields is the target sub-object's complete field list.
var specTargetFields = []string{"node", "session", "task"}

// TestApprovalTokenCanonicalJSONRecord asserts the canonical encoding
// carries exactly the specified fields, in sorted order, with no
// insignificant whitespace.
func TestApprovalTokenCanonicalJSONRecord(t *testing.T) {
	raw := CanonicalEncode(fixtureRecord())
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("the canonical encoding is not JSON: %v", err)
	}
	if len(probe) != len(specRecordFields) {
		t.Fatalf("the record carries %d fields, the specification names %d", len(probe), len(specRecordFields))
	}
	for _, name := range specRecordFields {
		if _, ok := probe[name]; !ok {
			t.Errorf("the canonical encoding is missing the specified field %q", name)
		}
	}
	var target map[string]json.RawMessage
	if err := json.Unmarshal(probe["target"], &target); err != nil {
		t.Fatalf("target is not an object: %v", err)
	}
	if len(target) != len(specTargetFields) {
		t.Errorf("target carries %d fields, the specification names %d", len(target), len(specTargetFields))
	}
	assertSortedNoWhitespace(t, string(raw))
}

// assertSortedNoWhitespace checks the two mechanical properties of the
// canonical form: keys ascend, and no space, tab or newline appears
// outside a string.
func assertSortedNoWhitespace(t *testing.T, encoded string) {
	t.Helper()
	if strings.ContainsAny(encoded, " \t\n\r") {
		t.Errorf("the canonical encoding carries insignificant whitespace: %s", encoded)
	}
	last := ""
	for _, name := range specRecordFields {
		at := strings.Index(encoded, `"`+name+`":`)
		if at < 0 {
			continue
		}
		if name < last {
			t.Errorf("field %q appears after %q; the canonical form sorts keys", name, last)
		}
		last = name
	}
}

// TestApprovalTokenVerbIsSigned proves R-21.209's confused-deputy defence:
// two records that differ ONLY in the verb produce different bytes, and a
// signature over one does not verify against the other.
func TestApprovalTokenVerbIsSigned(t *testing.T) {
	priv := ed25519.NewKeyFromSeed([]byte(fixtureKeySeed))
	pub, _ := priv.Public().(ed25519.PublicKey)
	v, err := NewApprovalVerifier(pub, fuzzClock())
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	first := fixtureRecord()
	second := fixtureRecord()
	second.Verb = "workspace.delete"
	if string(CanonicalEncode(first)) == string(CanonicalEncode(second)) {
		t.Fatal("two records differing only in the verb encoded identically")
	}
	swapped := append(append([]byte{}, CanonicalEncode(second)...), signFixture(priv, first)...)
	if _, err := v.Verify(swapped); err == nil {
		t.Fatal("a signature over one verb verified against another")
	}
}

// TestApprovalTokenRejectsNonCanonicalEncoding drives the three
// non-canonical shapes through Verify with a GOOD signature over the
// canonical bytes, so only the canonical check can be what refuses them.
func TestApprovalTokenRejectsNonCanonicalEncoding(t *testing.T) {
	priv := ed25519.NewKeyFromSeed([]byte(fixtureKeySeed))
	pub, _ := priv.Public().(ed25519.PublicKey)
	v, err := NewApprovalVerifier(pub, fuzzClock())
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	canonical := string(CanonicalEncode(fixtureRecord()))
	sig := signFixture(priv, fixtureRecord())
	for _, tc := range []struct {
		name    string
		payload string
		want    error
	}{
		{"re-spaced", strings.Replace(canonical, `{"approver":`, `{ "approver": `, 1), ErrNonCanonical},
		{"reordered", reorderFirstTwo(canonical), ErrNonCanonical},
		{"unknown field", strings.Replace(canonical, `,"verb":`, `,"extra":"x","verb":`, 1), ErrUnknownField},
		{"duplicate key", strings.Replace(canonical, `,"verb":`, `,"approver":"other","verb":`, 1), ErrDuplicateKey},
		{"trailing content", canonical + "{}", ErrNonCanonical},
	} {
		rec, verr := v.Verify(append([]byte(tc.payload), sig...))
		if rec != nil {
			t.Errorf("%s: Verify returned a record", tc.name)
		}
		if !errors.Is(verr, tc.want) {
			t.Errorf("%s: Verify = %v; want %v", tc.name, verr, tc.want)
		}
	}
}

// reorderFirstTwo swaps the first two members of the canonical object, so
// the bytes decode to the same record in a different order.
func reorderFirstTwo(canonical string) string {
	parts := strings.SplitN(strings.TrimPrefix(canonical, "{"), ",", 3)
	if len(parts) < 3 {
		return canonical
	}
	return "{" + parts[1] + "," + parts[0] + "," + parts[2]
}

// TestCanonicalDecodeRefusesUnreadableValues covers the typed-conversion
// half of the decoder: a timestamp, enum name or identifier it cannot read
// REFUSES rather than defaulting to a permissive value.
func TestCanonicalDecodeRefusesUnreadableValues(t *testing.T) {
	canonical := string(CanonicalEncode(fixtureRecord()))
	for _, tc := range []struct{ name, from, to string }{
		{"unreadable issued time", `"issued":"` + canonicalTime(fixtureIssued) + `"`, `"issued":"yesterday"`},
		{"unreadable expiry", `"expiry":"` + canonicalTime(fixtureIssued.Add(MaxApprovalTTL)) + `"`, `"expiry":"soon"`},
		{"unknown risk rung", `"risk":"L2"`, `"risk":"L9"`},
		{"invalid risk name", `"risk":"L2"`, `"risk":"invalid-risk-level"`},
		{"unknown sensitivity", `"sensitivity":"internal"`, `"sensitivity":"classified"`},
		{"malformed request id", `"request_id":"` + fixtureRequestID + `"`, `"request_id":"short"`},
		{"malformed nonce", `"nonce":"` + fixtureNonce + `"`, `"nonce":"nope"`},
	} {
		mutated := strings.Replace(canonical, tc.from, tc.to, 1)
		if mutated == canonical {
			t.Fatalf("%s: the fixture did not contain %s", tc.name, tc.from)
		}
		if _, err := canonicalDecode([]byte(mutated)); err == nil {
			t.Errorf("%s: canonicalDecode accepted a value it cannot read", tc.name)
		}
	}
}

// TestRejectDuplicateKeysWalksNestedShapes proves the duplicate-key scan
// is not fooled by strings that look like keys: a repeated key inside the
// nested target object is caught, and arrays and repeated string VALUES
// are not mistaken for repeated keys.
func TestRejectDuplicateKeysWalksNestedShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"clean", `{"a":"x","b":{"c":"x","d":["x","x"]}}`, false},
		{"repeated values", `{"a":"same","b":"same"}`, false},
		{"top-level duplicate", `{"a":1,"a":2}`, true},
		{"nested duplicate", `{"t":{"node":"a","node":"b"}}`, true},
		{"duplicate after an array", `{"l":[1,2],"k":1,"k":2}`, true},
		{"non-string key", `{"a":1,`, false},
	} {
		err := rejectDuplicateKeys([]byte(tc.raw))
		if got := errors.Is(err, ErrDuplicateKey); got != tc.want {
			t.Errorf("%s: duplicate detected = %v, want %v (err %v)", tc.name, got, tc.want, err)
		}
	}
}

// fixtureRequestID and fixtureNonce are the identifiers the checked-in
// corpus was captured with.
const (
	fixtureRequestID = "0123456789ABCDEFGHJKMNPQRS"
	fixtureNonce     = "TVWXYZ0123456789ABCDEFGHJK"
)

// fixtureRecord is the record every checked-in corpus entry is built from.
func fixtureRecord() ApprovalRecord {
	rec := sampleRecord()
	rec.SchemaVersion = ApprovalSchemaVersion
	rec.KeyID = "approval-fixture-1"
	rec.RequestID = fixtureRequestID
	rec.Nonce = fixtureNonce
	rec.Issued = fixtureIssued
	rec.Expiry = fixtureIssued.Add(MaxApprovalTTL)
	return rec
}

// fuzzClock is a clock frozen inside the fixture record's validity window.
func fuzzClock() Clock {
	return testkit.NewFrozenClock(fixtureIssued.Add(time.Minute))
}

// signFixture signs rec's canonical bytes in the approval domain.
func signFixture(priv ed25519.PrivateKey, rec ApprovalRecord) []byte {
	return ed25519.Sign(priv, signingInput(CanonicalEncode(rec)))
}

// TestWriteApprovalCorpus regenerates the named seed corpus. It is inert
// unless CASCADE_WRITE_APPROVAL_CORPUS is set, so a normal run never
// rewrites the fixtures it is asserting against.
func TestWriteApprovalCorpus(t *testing.T) {
	if os.Getenv("CASCADE_WRITE_APPROVAL_CORPUS") == "" {
		t.Skip("set CASCADE_WRITE_APPROVAL_CORPUS=1 to regenerate the seed corpus")
	}
	priv := ed25519.NewKeyFromSeed([]byte(fixtureKeySeed))
	for name, data := range corpusEntries(priv) {
		body := "go test fuzz v1\n[]byte(" + strconv.Quote(string(data)) + ")\n"
		if err := os.WriteFile(filepath.Join(fuzzCorpusDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

// corpusEntries builds the ten named seeds. Each one is a deliberate
// mutation of the same valid token, so a seed's name says exactly what it
// captures.
func corpusEntries(priv ed25519.PrivateKey) map[string][]byte {
	rec := fixtureRecord()
	payload := CanonicalEncode(rec)
	valid := append(append([]byte{}, payload...), signFixture(priv, rec)...)
	flippedSig := append([]byte{}, valid...)
	flippedSig[len(flippedSig)-1] ^= 0x01
	flippedNonce := append([]byte{}, valid...)
	flippedNonce[strings.Index(string(flippedNonce), `"nonce":"`)+9] = 'Z'
	expiredRec := fixtureRecord()
	expiredRec.Issued = fixtureIssued.Add(-24 * time.Hour)
	expiredRec.Expiry = expiredRec.Issued.Add(MaxApprovalTTL)
	sig := signFixture(priv, rec)
	mutate := func(from, to string) []byte {
		return append([]byte(strings.Replace(string(payload), from, to, 1)), sig...)
	}
	return map[string][]byte{
		"valid-signed.bin":           valid,
		"truncated.bin":              valid[:len(valid)/2],
		"flipped-signature-byte.bin": flippedSig,
		"flipped-nonce-byte.bin":     flippedNonce,
		"expired.bin": append(append([]byte{}, CanonicalEncode(expiredRec)...),
			signFixture(priv, expiredRec)...),
		"zero-length.bin":        {},
		"oversized.bin":          append(append([]byte{}, valid...), []byte(strings.Repeat("A", 70000))...),
		"non-canonical-json.bin": mutate(`{"approver":`, `{ "approver": `),
		"unknown-field.bin":      mutate(`,"verb":`, `,"unknown_field":"x","verb":`),
		"duplicate-key.bin":      mutate(`,"verb":`, `,"approver":"other","verb":`),
	}
}
