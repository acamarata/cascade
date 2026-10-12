package envelope

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// validWire returns the JSON of a valid Signed envelope.
func validWire(t *testing.T) []byte {
	t.Helper()
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	return mustJSON(t, issueAs(t, ctl, rootEnvelope(ctl)))
}

// FuzzDecodeSigned: Decode never panics, never allocates beyond
// 4 x MaxEncodedBytes, refuses only with KindInvalidInput, and every
// accepted input re-encodes to identical Canonical bytes.
func FuzzDecodeSigned(f *testing.F) {
	f.Add([]byte(`{"env":{"v":1},"sig":null}`))
	f.Add([]byte(`{"env":{"v":1,"roots":["/a"],"ext":[{"kind":"k","data":{"a":[1,2]}}]},"sig":"AA=="}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		s, err := Decode(b)
		runtime.ReadMemStats(&m1)
		if used := m1.TotalAlloc - m0.TotalAlloc; used > 4*MaxEncodedBytes {
			t.Fatalf("Decode of %d bytes allocated %d bytes", len(b), used)
		}
		if err != nil {
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("Decode refused with %v, want KindInvalidInput", err)
			}
			return
		}
		roundTrip(t, s)
	})
}

// roundTrip re-encodes an accepted envelope and requires identical
// canonical bytes and signature after a second Decode.
func roundTrip(t *testing.T, s Signed) {
	t.Helper()
	c1, err := Canonical(s.Env)
	if err != nil {
		t.Fatalf("accepted envelope has no canonical form: %v", err)
	}
	s2, err := Decode(mustJSON(t, s))
	if err != nil {
		t.Fatalf("re-encoded envelope refused: %v", err)
	}
	c2, err := Canonical(s2.Env)
	if err != nil || !bytes.Equal(c1, c2) || !bytes.Equal(s.Sig, s2.Sig) {
		t.Fatalf("re-encode changed the canonical bytes (err %v):\n%s\n%s", err, c1, c2)
	}
}

// TestDecodeAcceptsIssued proves Decode accepts what Issue produces and
// the result still verifies.
func TestDecodeAcceptsIssued(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	issued := issueAs(t, ctl, rootEnvelope(ctl))
	s, err := Decode(mustJSON(t, issued))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, err := Verify(s, ctl.PublicKey(), t0); err != nil {
		t.Fatalf("Verify(decoded) = %v", err)
	}
	roundTrip(t, s)
}

// TestDecodeRefuses covers each bound and strictness rule.
func TestDecodeRefuses(t *testing.T) {
	for _, tc := range decodeRefusals(t) {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(tc.in))
			wantKind(t, err, cascade.KindInvalidInput, tc.msg)
		})
	}
}

// decodeRefusal is one input Decode must refuse and its message.
type decodeRefusal struct{ name, in, msg string }

// decodeRefusals lists one input per bound and strictness rule.
func decodeRefusals(t *testing.T) []decodeRefusal {
	wire := string(validWire(t))
	const grant = `{"id":"a","verb":"b","exp":"2026-01-01T00:00:00Z"}`
	list := func(n int) string { return `["` + strings.Repeat(`a","`, n-1) + `a"]` }
	nested := strings.Repeat(`[`, maxDepth+1) + strings.Repeat(`]`, maxDepth+1)
	return []decodeRefusal{
		{"oversize", strings.Repeat(" ", MaxEncodedBytes+1), "over 65536"},
		{"invalid utf-8", "{\"env\":{\"id\":\"\xff\"}}", "not valid UTF-8"},
		{"unknown field", strings.Replace(wire, `"sig":`, `"extra":1,"sig":`, 1), "unknown field"},
		{"unknown env field", strings.Replace(wire, `"v":1,`, `"v":1,"admin":true,`, 1), "unknown field"},
		{"duplicate key", strings.Replace(wire, `"v":1,`, `"v":1,"v":1,`, 1), `duplicate key "v"`},
		{"case-variant key", strings.Replace(wire, `"sig":`, `"SIG":`, 1), `is not the exact field name "sig"`},
		{"kelvin-sign key", strings.Replace(wire, `"kind":`, "\"Kind\":", 1), `is not the exact field name "kind"`},
		{"escaped key", strings.Replace(wire, `"sig":`, "\"\\u0073ig\":", 1), "escaped key"},
		{"65 roots", `{"env":{"roots":` + list(65) + `}}`, "roots: more than 64"},
		{"257 tools", `{"env":{"tools":` + list(257) + `}}`, "tools: more than 256"},
		{"257 cmds", `{"env":{"cmds":` + list(257) + `}}`, "cmds: more than 256"},
		{"257 egress", `{"env":{"egress":` + list(257) + `}}`, "egress: more than 256"},
		{"33 grants", `{"env":{"grants":[` + strings.Repeat(grant+`,`, 32) + grant + `]}}`, "grants: more than 32"},
		{"33 ext", `{"env":{"ext":[` + strings.Repeat(`{"kind":"k","data":1},`, 32) + `{"kind":"k","data":1}]}}`, "ext: more than 32"},
		{"deep nesting", `{"env":{"ext":[{"data":` + nested + `}]}}`, "nesting deeper than 32"},
		{"trailing data", wire + ` {}`, "trailing data"},
		{"unsorted tools", strings.Replace(wire, `"tools":["edit","read"]`, `"tools":["read","edit"]`, 1), "tools: entries are not sorted"},
		{"dotdot root", strings.Replace(wire, `"/srv/work"`, `"/a/b/../../etc"`, 1), "not an absolute clean path"},
		{"control byte", "{\"env\":{\"id\":\"a\x01\"}}", "control byte"},
		{"unterminated", `{"env":{"id":"abc`, "unterminated string"},
		{"bad byte", `{"env":{"ext":[{"kind":"k","data":}]}}`, "unexpected byte"},
		{"null time", strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":null`, 1), "expected a string"},
		{"numeric tier", strings.Replace(wire, `"sens":"restricted"`, `"sens":0`, 1), "expected a string"},
		{"empty tier", strings.Replace(wire, `"sens":"restricted"`, `"sens":""`, 1), "is not a tier name"},
		{"one-digit hour", strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":"2026-01-02T3:04:05Z"`, 1), "is not RFC 3339"},
		{"comma fraction", strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":"2026-01-02T03:04:05,5Z"`, 1), "is not RFC 3339"},
		{"lowercase t", strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":"2026-01-02t03:04:05Z"`, 1), "is not RFC 3339"},
		{"ten-digit fraction", strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":"2026-01-02T03:04:05.1234567890Z"`, 1), "is not RFC 3339"},
		{"long number", strings.Replace(wire, `"tok":1000`, `"tok":`+strings.Repeat("1", 21), 1), "is too long"},
		{"string budget", strings.Replace(wire, `"tok":1000`, `"tok":"1000"`, 1), "expected a number"},
		{"null root entry", strings.Replace(wire, `"roots":["/a/b",`, `"roots":[null,`, 1), "expected a string"},
		{"re-escaped data key", strings.Replace(wire, `{"max":10}`, `{"<":1}`, 1), "re-escapes"},
		{"env not an object", `{"env":null,"sig":null}`, "expected an object"},
		{"budget null", strings.Replace(wire, `"budget":{`, `"budget":null,"x":{`, 1), "expected an object"},
		{"missing field", strings.Replace(wire, `"principal":"principal-1",`, ``, 1), `missing field "principal"`},
		{"missing sig", wire[:strings.Index(wire, `,"sig":`)] + `}`, `missing field "sig"`},
		{"sig length 63", sigLen(t, wire, 63), "signature has length 63"},
		{"ext data duplicate key", strings.Replace(wire, `{"max":10}`, `{"max":1,"max":99}`, 1), `duplicate key "max" in extension data`},
		{"ext data case-folded key", strings.Replace(wire, `{"max":10}`, `{"max":1,"MAX":99}`, 1), `duplicate key "MAX" in extension data`},
		{"ext data escaped key", strings.Replace(wire, `{"max":10}`, "{\"\\u006dax\":1}", 1), "escaped key"},
		{"ext data 65 keys", strings.Replace(wire, `{"max":10}`, manyKeys(65), 1), "more than 64 keys"},
		{"re-encode over the bound", strings.Replace(wire, `"id":"env-root"`, `"id":"`+strings.Repeat("<", 12000)+`"`, 1), "re-encoded envelope could reach"},
		{"wrong version", strings.Replace(wire, `"v":1,`, `"v":2,`, 1), "version 2 is not 1"},
		{"malformed", strings.Replace(wire, `"v":1,`, `"v":1x,`, 1), "decode"},
	}
}

// TestDuplicateExtensionKindRefused: two Ext entries of one kind (one
// narrow, one wide) are refused by Decode, Narrow and Canonical.
func TestDuplicateExtensionKindRefused(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	parent := rootEnvelope(ctl)
	dup := childEnvelope(t, parent)
	dup.Ext = []Extension{
		{Kind: testKind, Data: json.RawMessage(`{"max":1}`)},
		{Kind: testKind, Data: json.RawMessage(`{"max":99}`)},
	}
	const msg = `duplicate extension kind "test.budget"`
	_, err := Narrow(parent, dup)
	wantKind(t, err, cascade.KindInvalidInput, msg)
	_, err = Canonical(dup)
	wantKind(t, err, cascade.KindInvalidInput, msg)
	issued := issueAs(t, ctl, parent)
	wire := mustJSON(t, Signed{Env: dup, Sig: issued.Sig})
	_, err = Decode(wire)
	wantKind(t, err, cascade.KindInvalidInput, msg)
	if _, err := Issue(context.Background(), ctl, dup); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Issue(dup ext) = %v, want KindInvalidInput", err)
	}
}

// TestRegisterExtensionPanics covers the init-time registration guards.
func TestRegisterExtensionPanics(t *testing.T) {
	ok := func(json.RawMessage, json.RawMessage) error { return nil }
	for name, reg := range map[string]func(){
		"duplicate": func() { RegisterExtension(testKind, ok) },
		"empty":     func() { RegisterExtension("", ok) },
		"nil":       func() { RegisterExtension("test.nil", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("RegisterExtension did not panic")
				}
			}()
			reg()
		})
	}
}
