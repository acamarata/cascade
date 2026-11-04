package nodes

// Purpose (this file): the DispatchPayload codec refuses every malformed
//   payload with KindInvalidInput before anything runs, and keeps a
//   zero-sequence resume distinct from a cold start.
// SPORT: internal/nodes (ADD tests) — P1-NODE-02b.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// payloadSigner signs with a key generated for one test run.
type payloadSigner struct{ priv ed25519.PrivateKey }

func (s payloadSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

func (s payloadSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

// signedFor issues an envelope fenced to (dispatchID, attempt).
func signedFor(tb testing.TB, dispatchID string, attempt uint64) envelope.Signed {
	tb.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s, err := envelope.Issue(context.Background(), payloadSigner{priv}, envelope.Envelope{
		Version: envelope.Version, ID: "env-1", Principal: "principal-1",
		Roots: []string{"/srv/work"}, Tools: []string{"edit"},
		Budget:      envelope.Budget{WallClock: time.Hour, Tokens: 100},
		Fence:       envelope.Fence{DispatchID: dispatchID, Attempt: attempt, LeaseEpoch: 3},
		Sensitivity: provider.SensitivityRestricted,
		IssuedAt:    t0, ExpiresAt: t0.Add(time.Hour),
	})
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// validPayload is a payload every check accepts.
func validPayload(tb testing.TB) DispatchPayload {
	tb.Helper()
	return DispatchPayload{
		Version: PayloadVersion, DispatchID: "d-1", Attempt: 2,
		ActionID: "act-1", Kind: ActionAgent, EntityID: "job-1",
		Head:       strings.Repeat("ab", 20),
		RepoRemote: "ssh://controller/repo.git",
		ResultRef:  "refs/cascade/results/d-1/2",
		Spec:       json.RawMessage(`{"prompt":"x"}`),
		Envelope:   signedFor(tb, "d-1", 2),
	}
}

// mustEncode encodes p or fails the test.
func mustEncode(tb testing.TB, p DispatchPayload) []byte {
	tb.Helper()
	b, err := EncodePayload(p)
	if err != nil {
		tb.Fatalf("EncodePayload: %v", err)
	}
	return b
}

// wantInvalid asserts err is a KindInvalidInput refusal.
func wantInvalid(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: DecodePayload accepted it", name)
		return
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("%s: kind = %v (%v), want KindInvalidInput", name, kind, err)
	}
}

// TestP1RoutedDispatchPayload: every malformed payload fails DecodePayload
// with KindInvalidInput; the valid one round-trips and its Digest is the
// sha256 of its wire bytes.
func TestP1RoutedDispatchPayload(t *testing.T) {
	good := validPayload(t)
	b := mustEncode(t, good)
	got, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("valid payload refused: %v", err)
	}
	sum := sha256.Sum256(b)
	if got.Digest() != hex.EncodeToString(sum[:]) || good.Digest() != got.Digest() {
		t.Fatalf("digest %q is not the sha256 of the wire bytes", got.Digest())
	}
	if got.Envelope.Env.Fence.DispatchID != "d-1" || got.Envelope.Env.Fence.Attempt != 2 {
		t.Fatalf("decoded fence = %+v", got.Envelope.Env.Fence)
	}

	for name, raw := range rawRefusals(t, b) {
		_, err := DecodePayload(raw)
		wantInvalid(t, name, err)
	}
	for name, mutate := range fieldRefusals() {
		p := validPayload(t)
		mutate(&p)
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		_, err = DecodePayload(raw)
		wantInvalid(t, name, err)
		if _, err := EncodePayload(p); err == nil {
			t.Errorf("%s: EncodePayload produced a payload its peer refuses", name)
		}
	}
}

// rawRefusals are byte-level corruptions of the valid encoding b.
func rawRefusals(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	var indented bytes.Buffer
	if err := json.Indent(&indented, b, "", " "); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat(" ", MaxPayloadBytes)
	return map[string][]byte{
		"oversized":            append(append([]byte{}, b[:len(b)-1]...), pad+"}"...),
		"unknown field":        bytes.Replace(b, []byte(`{"v":1,`), []byte(`{"v":1,"extra":true,`), 1),
		"unknown resume field": bytes.Replace(b, []byte(`"spec":`), []byte(`"resume":{"bogus":1},"spec":`), 1),
		"wrong version":        bytes.Replace(b, []byte(`{"v":1,`), []byte(`{"v":2,`), 1),
		"digest: re-spaced":    indented.Bytes(),
		"digest: re-cased key": bytes.Replace(b, []byte(`"head"`), []byte(`"HEAD"`), 1),
		"digest: duplicate":    bytes.Replace(b, []byte(`{"v":1,`), []byte(`{"v":1,"v":1,`), 1),
		"trailing data":        append(append([]byte{}, b...), `{}`...),
		"empty":                nil,
		"not json":             []byte("dispatch"),
		"invalid utf-8":        bytes.Replace(b, []byte(`act-1`), []byte("act-\xff"), 1),
		"null envelope":        envelopeAs(t, b, "null"),
		"unsigned envelope":    bytes.Replace(b, []byte(`"sig":"`), []byte(`"sig":"AAAA`), 1),
	}
}

// envelopeAs replaces the envelope value in b with v.
func envelopeAs(t *testing.T, b []byte, v string) []byte {
	t.Helper()
	i := bytes.Index(b, []byte(`"envelope":`))
	if i < 0 {
		t.Fatal("no envelope key")
	}
	return append(append([]byte{}, b[:i+len(`"envelope":`)]...), v+"}"...)
}

// fieldRefusals mutate one field of a valid payload into a refused one.
func fieldRefusals() map[string]func(*DispatchPayload) {
	return map[string]func(*DispatchPayload){
		"version 0":           func(p *DispatchPayload) { p.Version = 0 },
		"unknown kind":        func(p *DispatchPayload) { p.Kind = "shell" },
		"upper-case head":     func(p *DispatchPayload) { p.Head = strings.ToUpper(p.Head) },
		"short head":          func(p *DispatchPayload) { p.Head = p.Head[:39] },
		"non-hex head":        func(p *DispatchPayload) { p.Head = strings.Repeat("zz", 20) },
		"empty dispatch id":   func(p *DispatchPayload) { p.DispatchID = "" },
		"slash dispatch id":   func(p *DispatchPayload) { p.DispatchID = "d/1" },
		"zero attempt":        func(p *DispatchPayload) { p.Attempt = 0 },
		"empty action id":     func(p *DispatchPayload) { p.ActionID = "" },
		"empty entity id":     func(p *DispatchPayload) { p.EntityID = "" },
		"empty repo remote":   func(p *DispatchPayload) { p.RepoRemote = "" },
		"foreign result ref":  func(p *DispatchPayload) { p.ResultRef = "refs/cascade/results/d-1/3" },
		"fence: other id":     func(p *DispatchPayload) { p.Envelope.Env.Fence.DispatchID = "d-2" },
		"fence: old attempt":  func(p *DispatchPayload) { p.Envelope.Env.Fence.Attempt = 1 },
		"fence: wrong record": func(p *DispatchPayload) { p.Attempt = 3; p.ResultRef = "refs/cascade/results/d-1/3" },
		"unsorted envelope":   func(p *DispatchPayload) { p.Envelope.Env.Tools = []string{"read", "edit"} },
		"short signature":     func(p *DispatchPayload) { p.Envelope.Sig = p.Envelope.Sig[:10] },
	}
}

// TestResumeZeroSequenceIsNotColdStart: a resume point with sequence 0 and
// completed operations travels as a resume; a zero point is a cold start
// and is omitted from the wire.
func TestResumeZeroSequenceIsNotColdStart(t *testing.T) {
	resumed := validPayload(t)
	resumed.Resume = ResumePoint{EntityID: "job-1", Seq: 0, CompletedOperations: []string{"op-a"}}
	b := mustEncode(t, resumed)
	got, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("resume payload refused: %v", err)
	}
	if reflect.ValueOf(got.Resume).IsZero() {
		t.Fatal("a zero-sequence resume decoded as a cold start")
	}
	if got.Resume.Seq != 0 || !got.Resume.Completed("op-a") || got.Resume.FromScratch {
		t.Fatalf("resume = %+v, want seq 0 with op-a completed", got.Resume)
	}

	cold := validPayload(t)
	cb := mustEncode(t, cold)
	if bytes.Contains(cb, []byte(`"resume"`)) {
		t.Fatalf("a cold start put a resume point on the wire: %s", cb)
	}
	gotCold, err := DecodePayload(cb)
	if err != nil {
		t.Fatalf("cold payload refused: %v", err)
	}
	if !reflect.ValueOf(gotCold.Resume).IsZero() {
		t.Fatalf("cold start decoded with a resume point: %+v", gotCold.Resume)
	}
	if cold.Digest() == resumed.Digest() {
		t.Fatal("cold start and resume share a digest")
	}
}
