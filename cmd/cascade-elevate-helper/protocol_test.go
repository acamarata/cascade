// Purpose: tests for the helper protocol against an injected backend and
//
//	caller check: round trips, strict decoding, the caller gate in front of
//	every key op, the enrolled-key guard, presence before delete and the
//	helper-built prompt.
//
// SPORT: cmd/cascade-elevate-helper protocol tests/ADDED (P1-SHIP-15).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeBackend records every call and answers from fields.
type fakeBackend struct {
	available, cgo bool
	tier           string
	enrolled       bool
	absentErr      error // what EnrollmentAbsent says when nothing is enrolled
	presence       error
	signErr        error
	genErr         error // what Generate says
	calls          []string
	prompt         string
	signed         []byte
}

func (f *fakeBackend) Available() bool { return f.available }
func (f *fakeBackend) Tier() string    { return f.tier }
func (f *fakeBackend) CGO() bool       { return f.cgo }

func (f *fakeBackend) Pubkey() (string, error) {
	f.calls = append(f.calls, "pubkey")
	if !f.enrolled {
		return "", elevation.ErrHelperNotEnrolled()
	}
	return base64.StdEncoding.EncodeToString(make([]byte, 32)), nil
}

func (f *fakeBackend) EnrollmentAbsent() error {
	f.calls = append(f.calls, "absent")
	if f.enrolled {
		return elevation.ErrAlreadyEnrolled()
	}
	return f.absentErr
}

func (f *fakeBackend) Generate() error {
	f.calls = append(f.calls, "generate")
	if f.genErr != nil {
		return f.genErr
	}
	f.enrolled = true
	return nil
}

func (f *fakeBackend) Sign(payload []byte, reason string) ([]byte, error) {
	f.calls = append(f.calls, "sign")
	f.prompt, f.signed = reason, payload
	return make([]byte, 64), f.signErr
}

func (f *fakeBackend) Presence(string) error {
	f.calls = append(f.calls, "presence")
	return f.presence
}

func (f *fakeBackend) DeleteKey() error {
	f.calls = append(f.calls, "delete")
	return nil
}

func allowCaller() error { return nil }

func refuseCaller() error {
	return cascade.New(cascade.KindPermissionDenied, "caller is not signed by this helper's team")
}

func newFake() *fakeBackend {
	return &fakeBackend{available: true, cgo: true, tier: string(elevation.TierOSKeychain), enrolled: true}
}

// ask sends one request line and returns the decoded reply.
func ask(t *testing.T, s server, line string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.serve(strings.NewReader(line+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("reply is not exactly one line: %q", out.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("reply is not JSON: %v: %q", err, out.String())
	}
	return m
}

// str returns m[key] when it is a string, else "" (a missing field is "").
func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func wantFailure(t *testing.T, m map[string]any, kind string) {
	t.Helper()
	if m["ok"] != false || m["kind"] != kind || m["v"] != float64(1) {
		t.Fatalf("reply = %v, want ok=false kind=%s", m, kind)
	}
}

func payloadReq(payload []byte) string {
	return `{"v":1,"op":"sign","payload_b64":"` + base64.StdEncoding.EncodeToString(payload) + `"}`
}

func TestHelperProtocol(t *testing.T) {
	fb := newFake()
	s := server{be: fb, caller: allowCaller}
	if m := ask(t, s, `{"v":1,"op":"status"}`); m["ok"] != true || m["available"] != true || m["tier"] != "os-keychain" || m["cgo"] != true {
		t.Fatalf("status = %v", m)
	}
	if m := ask(t, s, `{"v":1,"op":"pubkey"}`); m["ok"] != true || str(m, "pub_b64") == "" {
		t.Fatalf("pubkey = %v", m)
	}
	if m := ask(t, s, payloadReq([]byte("hello"))); m["ok"] != true || str(m, "sig_b64") == "" {
		t.Fatalf("sign = %v", m)
	}
	if m := ask(t, s, `{"v":1,"op":"delete"}`); m["ok"] != true {
		t.Fatalf("delete = %v", m)
	}
	fb.enrolled, fb.calls = false, nil
	if m := ask(t, s, `{"v":1,"op":"generate"}`); m["ok"] != true {
		t.Fatalf("generate = %v", m)
	}
	if strings.Join(fb.calls, ",") != "absent,generate" || !fb.enrolled {
		t.Fatalf("generate must check the enrollment state, then generate once: calls %v enrolled %v", fb.calls, fb.enrolled)
	}
	invalid := map[string]string{
		"unknown op":       `{"v":1,"op":"export"}`,
		"invalid json":     `{"v":1,"op":`,
		"not an object":    `[1]`,
		"empty line":       ``,
		"wrong version":    `{"v":2,"op":"status"}`,
		"trailing data":    `{"v":1,"op":"status"} {"v":1}`,
		"65 KiB line":      `{"v":1,"op":"status"` + strings.Repeat(" ", 65<<10) + `}`,
		"5000 byte sign":   payloadReq(make([]byte, 5000)),
		"payload on pub":   `{"v":1,"op":"pubkey","payload_b64":"AAAA"}`,
		"sign no payload":  `{"v":1,"op":"sign"}`,
		"sign bad base64":  `{"v":1,"op":"sign","payload_b64":"!!!"}`,
		"sign empty":       `{"v":1,"op":"sign","payload_b64":""}`,
		"unknown field":    `{"v":1,"op":"status","reason":"x"}`,
		"huge base64 text": `{"v":1,"op":"sign","payload_b64":"` + strings.Repeat("A", 70<<10) + `"}`,
	}
	for name, line := range invalid {
		t.Run(name, func(t *testing.T) { wantFailure(t, ask(t, s, line), "invalid-input") })
	}
	if m := ask(t, s, payloadReq(make([]byte, 4096))); m["ok"] != true {
		t.Fatalf("a 4096-byte payload must be accepted: %v", m)
	}
}

func TestHelperRefusesUnsignedCaller(t *testing.T) {
	fb := newFake()
	s := server{be: fb, caller: refuseCaller}
	for _, line := range []string{`{"v":1,"op":"pubkey"}`, payloadReq([]byte("x")), `{"v":1,"op":"generate"}`, `{"v":1,"op":"delete"}`} {
		m := ask(t, s, line)
		wantFailure(t, m, "permission-denied")
		if m["message"] != "caller is not signed by this helper's team" {
			t.Fatalf("message = %q", m["message"])
		}
	}
	if len(fb.calls) != 0 {
		t.Fatalf("a refused caller reached the keystore: %v", fb.calls)
	}
	if m := ask(t, s, `{"v":1,"op":"status"}`); m["ok"] != true {
		t.Fatalf("status must answer any caller: %v", m)
	}
}

func TestHelperGenerateRefusesWhenEnrolled(t *testing.T) {
	fb := newFake()
	s := server{be: fb, caller: allowCaller}
	wantFailure(t, ask(t, s, `{"v":1,"op":"generate"}`), "conflict")
	// An enrollment state that cannot be told (unreadable item, unknown
	// status) must refuse too: generate replaces an item without a presence
	// check.
	fb.enrolled, fb.absentErr = false, cascade.New(cascade.KindUnavailable, "state unknown")
	wantFailure(t, ask(t, s, `{"v":1,"op":"generate"}`), "unavailable")
	for _, c := range fb.calls {
		if c == "generate" {
			t.Fatal("generate ran although the enrollment state was not known to be empty")
		}
	}
}

func TestHelperGenerateFailureIsReported(t *testing.T) {
	fb := newFake()
	fb.enrolled, fb.genErr = false, cascade.New(cascade.KindUnavailable, "x")
	s := server{be: fb, caller: allowCaller}
	wantFailure(t, ask(t, s, `{"v":1,"op":"generate"}`), "unavailable")
	if strings.Join(fb.calls, ",") != "absent,generate" || fb.enrolled {
		t.Fatalf("calls %v enrolled %v, want absent,generate and nothing enrolled", fb.calls, fb.enrolled)
	}
}

func TestHelperDeleteRequiresPresence(t *testing.T) {
	fb := newFake()
	fb.presence = elevation.ErrAuthFailed(nil)
	s := server{be: fb, caller: allowCaller}
	wantFailure(t, ask(t, s, `{"v":1,"op":"delete"}`), "permission-denied")
	if strings.Join(fb.calls, ",") != "presence" {
		t.Fatalf("calls after a refused presence = %v, want only presence", fb.calls)
	}
	fb.presence, fb.calls = nil, nil
	if m := ask(t, s, `{"v":1,"op":"delete"}`); m["ok"] != true {
		t.Fatalf("delete = %v", m)
	}
	if strings.Join(fb.calls, ",") != "presence,delete" {
		t.Fatalf("calls = %v, want presence before delete", fb.calls)
	}
}

func TestHelperPromptDerivedFromPayload(t *testing.T) {
	fb := newFake()
	s := server{be: fb, caller: allowCaller}
	payload := []byte("sign this exact payload")
	if m := ask(t, s, payloadReq(payload)); m["ok"] != true {
		t.Fatalf("sign = %v", m)
	}
	if fb.prompt != elevation.HelperPromptReason(payload) || !strings.HasPrefix(fb.prompt, "Cascade elevation ") || len(fb.prompt) != len("Cascade elevation ")+12 {
		t.Fatalf("prompt = %q", fb.prompt)
	}
	if !bytes.Equal(fb.signed, payload) {
		t.Fatal("the backend signed different bytes than the request carried")
	}
	fb.calls = nil
	for _, field := range []string{"reason", "prompt", "message", "text"} {
		wantFailure(t, ask(t, s, `{"v":1,"op":"sign","payload_b64":"AAAA","`+field+`":"Approve a harmless update"}`), "invalid-input")
	}
	if len(fb.calls) != 0 {
		t.Fatalf("a request with caller text reached the keystore: %v", fb.calls)
	}
}

func TestHelperStatusReportsCgo(t *testing.T) {
	for _, cgo := range []bool{true, false} {
		fb := newFake()
		fb.cgo, fb.available = cgo, cgo
		m := ask(t, server{be: fb, caller: refuseCaller}, `{"v":1,"op":"status"}`)
		if m["cgo"] != cgo || m["available"] != cgo {
			t.Fatalf("cgo=%v status = %v", cgo, m)
		}
	}
}

func TestHelperMapsBackendErrors(t *testing.T) {
	fb := newFake()
	s := server{be: fb, caller: allowCaller}
	fb.signErr = elevation.ErrAuthFailed(nil)
	wantFailure(t, ask(t, s, payloadReq([]byte("x"))), "permission-denied")
	fb.signErr = errors.New("raw failure\x00 with control bytes")
	wantFailure(t, ask(t, s, payloadReq([]byte("x"))), "unavailable")
}
