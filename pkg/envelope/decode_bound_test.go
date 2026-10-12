package envelope

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// sigLen returns wire with its signature replaced by n zero bytes.
func sigLen(t *testing.T, wire string, n int) string {
	t.Helper()
	i := strings.Index(wire, `"sig":"`)
	if i < 0 {
		t.Fatal("wire has no sig")
	}
	return wire[:i] + `"sig":"` + base64.StdEncoding.EncodeToString(make([]byte, n)) + `"}`
}

// manyKeys returns a JSON object with n distinct keys.
func manyKeys(n int) string {
	var b strings.Builder
	b.WriteString("{")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%d":%d`, i, i)
	}
	b.WriteString("}")
	return b.String()
}

// TestDecodeBoundedAllocation feeds near-limit inputs that pass the
// prescan, shaped to maximise decoded size, and checks the allocation
// bound outside the fuzzer too.
func TestDecodeBoundedAllocation(t *testing.T) {
	wire := string(validWire(t))
	room := MaxEncodedBytes - len(wire) - 16
	tools := `"tools":["` + strings.Repeat(strings.Repeat("t", 200)+`","`, 255) + `t"]`
	inputs := map[string]string{
		"long id":         strings.Replace(wire, `"env-root"`, `"`+strings.Repeat("x", room)+`"`, 1),
		"long ext data":   strings.Replace(wire, `{"max":10}`, `"`+strings.Repeat("x", room)+`"`, 1),
		"long sig":        sigLen(t, wire, room*3/4),
		"many tools":      strings.Replace(wire, `"tools":["edit","read"]`, tools, 1),
		"many data items": strings.Replace(wire, `{"max":10}`, `[`+strings.Repeat(`0,`, room/2)+`0]`, 1),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			if len(in) > MaxEncodedBytes || len(in) < MaxEncodedBytes*3/4 {
				t.Fatalf("fixture is %d bytes", len(in))
			}
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			_, _ = Decode([]byte(in))
			runtime.ReadMemStats(&m1)
			used := m1.TotalAlloc - m0.TotalAlloc
			if used > 4*MaxEncodedBytes {
				t.Fatalf("Decode allocated %d bytes, over %d", used, 4*MaxEncodedBytes)
			}
			t.Logf("%d-byte input: Decode allocated %d bytes", len(in), used)
		})
	}
}

// TestDecodeEscapeGrowthRoundTrips: raw "<" characters that json.Marshal
// re-escapes are accepted while the re-encoding fits, and that input
// re-encodes to identical canonical bytes.
func TestDecodeEscapeGrowthRoundTrips(t *testing.T) {
	wire := string(validWire(t))
	in := strings.Replace(wire, `"id":"env-root"`, `"id":"`+strings.Repeat("<", 3000)+`"`, 1)
	s, err := Decode([]byte(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	roundTrip(t, s)
}

// TestDecodeNormalises: Decode returns UTC times and compact extension
// data, so the decoded value equals its canonical form.
func TestDecodeNormalises(t *testing.T) {
	wire := string(validWire(t))
	in := strings.Replace(wire, `{"max":10}`, `{ "max" : 10 }`, 1)
	in = strings.Replace(in, `"exp":"2026-01-02T05:04:05Z"}`, `"exp":"2026-01-02T10:04:05+05:00"}`, 1)
	s, err := Decode([]byte(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Env.ExpiresAt.Location() != time.UTC || !s.Env.ExpiresAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want %v in UTC", s.Env.ExpiresAt, t0.Add(2*time.Hour))
	}
	if string(s.Env.Ext[0].Data) != `{"max":10}` {
		t.Fatalf("ext data = %s, want compact", s.Env.Ext[0].Data)
	}
}

// TestIssueSizeBound: Issue refuses a canonical form whose signed wire
// could not be decoded, and accepts the largest one that can.
func TestIssueSizeBound(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	e := rootEnvelope(ctl)
	c, err := Canonical(e)
	if err != nil {
		t.Fatal(err)
	}
	pad := maxCanonicalBody - (len(c) - len(domainSeparator))
	e.ID += strings.Repeat("x", pad)
	s := issueAs(t, ctl, e)
	wire, err := json.Marshal(s)
	if err != nil || len(wire) != MaxEncodedBytes {
		t.Fatalf("largest issued wire is %d bytes (err %v), want %d", len(wire), err, MaxEncodedBytes)
	}
	if _, err := Decode(wire); err != nil {
		t.Fatalf("Decode(largest issued) = %v", err)
	}
	e.ID += "x"
	_, err = Issue(context.Background(), ctl, e)
	wantKind(t, err, cascade.KindInvalidInput, "canonical form is")
}

// TestIssueRefusesWhatDecodeRefuses: Issue, Canonical and Verify apply
// the decoder's text and extension-data rules, so no signed envelope is
// undecodable and no two envelopes share canonical bytes.
func TestIssueRefusesWhatDecodeRefuses(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	cases := map[string]struct {
		mutate func(e *Envelope)
		msg    string
	}{
		"invalid utf-8 root":   {func(e *Envelope) { e.Roots = []string{"/a/\xff"} }, "not valid UTF-8"},
		"invalid utf-8 grant":  {func(e *Envelope) { e.Grants[0].Verb = "r\xfe" }, "not valid UTF-8"},
		"duplicate data key":   {func(e *Envelope) { e.Ext[0].Data = json.RawMessage(`{"x":1,"x":2}`) }, "duplicate key"},
		"case-folded data key": {func(e *Envelope) { e.Ext[0].Data = json.RawMessage(`{"x":1,"X":2}`) }, "duplicate key"},
		"re-escaped data key":  {func(e *Envelope) { e.Ext[0].Data = json.RawMessage(`{"a<b":1}`) }, "re-escapes"},
		"escaped data key":     {func(e *Envelope) { e.Ext[0].Data = json.RawMessage("{\"\\u0061\":1}") }, "escaped key"},
		"deep data":            {func(e *Envelope) { e.Ext[0].Data = json.RawMessage(strings.Repeat("[", 33) + strings.Repeat("]", 33)) }, "nesting deeper"},
		"invalid json data":    {func(e *Envelope) { e.Ext[0].Data = json.RawMessage(`{`) }, "not valid JSON"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := rootEnvelope(ctl)
			tc.mutate(&e)
			_, err := Issue(context.Background(), ctl, e)
			wantKind(t, err, cascade.KindInvalidInput, tc.msg)
			_, err = Verify(Signed{Env: e, Sig: make([]byte, 64)}, ctl.PublicKey(), t0)
			wantKind(t, err, cascade.KindIntegrity, tc.msg)
		})
	}
}

// TestDecodeRefusalsStayBounded: a refusal quotes at most clipLen bytes
// of input and allocates within the bound, even for huge offending text.
func TestDecodeRefusalsStayBounded(t *testing.T) {
	wire := string(validWire(t))
	del := strings.Repeat("\x7f", 60000)
	inputs := map[string]string{
		"unclean root": strings.Replace(wire, `"/srv/work"`, `"/srv/`+del+`/"`, 1),
		"del tier":     strings.Replace(wire, `"sens":"restricted"`, `"sens":"`+del+`"`, 1),
		"del ext kind": strings.Replace(wire, `"kind":"test.budget"`, `"kind":"`+del[:30000]+`"},{"kind":"`+del[:30000]+`","data":1`, 1),
		"del time":     strings.Replace(wire, `"iat":"2026-01-02T03:04:05Z"`, `"iat":"`+del+`"`, 1),
		"long key":     strings.Replace(wire, `"sig":`, `"`+del+`":1,"sig":`, 1),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			_, err := Decode([]byte(in))
			runtime.ReadMemStats(&m1)
			if err == nil {
				t.Fatal("Decode accepted the input")
			}
			if used := m1.TotalAlloc - m0.TotalAlloc; used > 4*MaxEncodedBytes {
				t.Fatalf("refusal allocated %d bytes, over %d", used, 4*MaxEncodedBytes)
			}
			if n := len(err.Error()); n > 4*clipLen+200 {
				t.Fatalf("error message is %d bytes: %.120s", n, err)
			}
		})
	}
}
