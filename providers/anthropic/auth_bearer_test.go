package anthropic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestBearerKeyHeader exercises the real request path without opening sockets.
func TestBearerKeyHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	canary := hex.EncodeToString(random[:])
	doer := &fakeDoer{queue: []fakeResp{{status: 401, body: `{"type":"error","error":{"type":"authentication_error","message":"bad token"}}`}}}
	broker := &fakeBroker{}
	d, err := New(Config{Doer: doer, Clock: fixedClock(time.Unix(1000, 0)), BaseURL: "https://gateway.invalid",
		Auth: AuthConfig{Mode: AuthModeBearerKey, KeyRef: "gateway:acct", Broker: broker,
			Resolver: fakeResolver{values: map[string]string{"gateway:acct": canary}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Chat(context.Background(), provider.ChatRequest{Model: "model-a", Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("unexpected error kind: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("requests = %d", len(doer.reqs))
	}
	h := doer.reqs[0].Headers
	if h["authorization"] != "Bearer "+canary || h["x-api-key"] != "" {
		t.Fatal("incorrect bearer headers")
	}
	if atomic.LoadInt32(&broker.starts) != 0 || atomic.LoadInt32(&broker.refreshes) != 0 {
		t.Fatal("broker was called")
	}
}

// TestPostureForBearerKey pins the public capabilities and auth vocabulary.
func TestPostureForBearerKey(t *testing.T) {
	if !AuthModeBearerKey.Valid() || AuthModeBearerKey.String() != "bearer-key" {
		t.Fatal("invalid bearer mode")
	}
	d := newTestDriver(t, AuthConfig{Mode: AuthModeBearerKey, KeyRef: "gateway:acct", Resolver: fakeResolver{}}, fixedClock(time.Unix(1000, 0)))
	caps, err := d.Capabilities(context.Background(), "model-a")
	if err != nil {
		t.Fatal(err)
	}
	p := caps.CompliancePosture
	if len(p.AuthModes) != 1 || p.AuthModes[0] != "bearer-key" || p.InteractiveEntitlement || !p.ProgrammaticEntitlement {
		t.Fatalf("unexpected posture: %+v", p)
	}
}
