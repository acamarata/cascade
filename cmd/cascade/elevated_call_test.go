//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

type countingCallKeystore struct {
	signingKeystore
	signs int
	hash  string
}

func (k *countingCallKeystore) Sign(p []byte) ([]byte, error) {
	k.signs++
	var fields struct {
		ActionHash string `json:"action_hash"`
	}
	if err := json.Unmarshal(p, &fields); err != nil {
		return nil, err
	}
	k.hash = fields.ActionHash
	return k.signingKeystore.Sign(p)
}

func callTestDeps(t *testing.T, tier elevation.CustodyTier) (elevatedCallDeps, *countingCallKeystore) {
	t.Helper()
	k := &countingCallKeystore{signingKeystore: newSigningKeystore(t)}
	return elevatedCallDeps{Custody: testCustody(t, k, tier), Clock: runtime.NewFixedClock(time.Unix(1_700_000_000, 0)), Getenv: func(string) string { return "" }}, k
}

func callTestClient(t *testing.T, h http.Handler) (*client.Client, *atomic.Int32) {
	t.Helper()
	path := shortUnixSocketPath(t)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := new(atomic.Int32)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); h.ServeHTTP(w, r) }))
	_ = s.Listener.Close()
	s.Listener = l
	s.Config.ConnContext = rpc.ConnContext
	s.Start()
	t.Cleanup(s.Close)
	return client.New(path, client.UnixDialer, time.Second), requests
}

func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	alias, err := os.MkdirTemp("", "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(alias) })
	link := filepath.Join(alias, "d")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(link, "rpc.sock")
}

func challengeClient(t *testing.T) (*client.Client, *atomic.Int32) {
	t.Helper()
	return callTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"code":-32008,"message":"required","data":{"nonce":"challenge"}}}`))
	}))
}

func TestCallElevatedRoundTripAgainstRealMiddleware(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	// Two always-elevated verbs, so an attestation signed for any verb but
	// the one dialed is refused by the real middleware.
	for _, method := range []string{"plugin.add", "plugin.update"} {
		t.Run(method, func(t *testing.T) {
			deps, k := callTestDeps(t, elevation.CustodyPlatform)
			pub, _ := k.PubKeyB64()
			fp, _ := elevation.Fingerprint(pub)
			registry := rpc.NewRegistry()
			registry.Use(rpc.ElevationMiddleware(rpc.ElevationDeps{Ledger: rpc.NewNonceLedger(deps.Clock), Trust: rpc.MapTrustStore{fp: k.pub}, Clock: deps.Clock, Audit: rpc.LocalCeremonyAudit{}, Custody: func() (string, bool) { return "platform", true }}))
			handled := new(atomic.Int32)
			var verifiedHash string
			registry.Register(method, func(_ context.Context, p json.RawMessage) (any, error) {
				handled.Add(1)
				verifiedHash = (&rpc.Request{Params: p}).ParamsHash()
				return "installed", nil
			})
			c, requests := callTestClient(t, rpc.NewHandler(registry))
			var out string
			err := callElevated(t.Context(), c, deps, method, map[string]string{"process_tier": "<>&"}, &out)
			if err != nil || requests.Load() != 2 || k.signs != 1 || handled.Load() != 1 || out != "installed" || verifiedHash == "" || verifiedHash != k.hash {
				t.Fatalf("err=%v requests=%d signs=%d handled=%d result=%q hashes=%s/%s", err, requests.Load(), k.signs, handled.Load(), out, verifiedHash, k.hash)
			}
		})
	}
}

func TestCallElevatedNoInputRefusesBeforeKeystore(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	deps.Getenv = func(string) string { return "1" }
	calls := 0
	custody := deps.Custody
	deps.Custody = func() elevation.Custody { calls++; return custody() }
	c, requests := challengeClient(t)
	err := callElevated(t.Context(), c, deps, "plugin.add", nil, nil)
	want := elevation.ErrNoInput()
	if !errors.Is(err, want) || err.Error() != want.Error() || calls != 0 || k.signs != 0 || requests.Load() != 1 {
		t.Fatalf("err=%v custody=%d signs=%d requests=%d", err, calls, k.signs, requests.Load())
	}
}

func TestCallElevatedNilGetenvReadsProcessEnv(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	deps.Getenv = nil
	t.Setenv("CASCADE_NO_INPUT", "1")
	calls := 0
	custody := deps.Custody
	deps.Custody = func() elevation.Custody { calls++; return custody() }
	c, requests := challengeClient(t)
	err := callElevated(t.Context(), c, deps, "plugin.add", nil, nil)
	want := elevation.ErrNoInput()
	if !errors.Is(err, want) || err.Error() != want.Error() || calls != 0 || k.signs != 0 || requests.Load() != 1 {
		t.Fatalf("err=%v custody=%d signs=%d requests=%d", err, calls, k.signs, requests.Load())
	}
}

func TestCallElevatedFileTierNeverSigns(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	deps, k := callTestDeps(t, elevation.CustodyFile)
	c, requests := challengeClient(t)
	err := callElevated(t.Context(), c, deps, "plugin.add", nil, nil)
	tier, ok := elevation.CustodyTierOf(err)
	if !ok || tier != elevation.CustodyFile || k.signs != 0 || requests.Load() != 1 {
		t.Fatalf("err=%v tier=%s signs=%d requests=%d", err, tier, k.signs, requests.Load())
	}
}

func TestCallElevatedWindowsTier2(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	calls := 0
	custody := deps.Custody
	deps.Custody = func() elevation.Custody { calls++; return custody() }
	c, requests := challengeClient(t)
	err := callElevated(t.Context(), c, deps, "plugin.add", nil, nil)
	want := elevation.ErrWindowsTier2()
	if !errors.Is(err, want) || err.Error() != want.Error() || calls != 0 || k.signs != 0 || requests.Load() != 1 {
		t.Fatalf("err=%v custody=%d signs=%d requests=%d", err, calls, k.signs, requests.Load())
	}
}

func TestSignedBytesMatchVerifier(t *testing.T) {
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	ledger := rpc.NewNonceLedger(deps.Clock)
	nonce, err := ledger.Issue("plugin.add", "hash")
	if err != nil {
		t.Fatal(err)
	}
	att, err := signAttestation(k, deps.Clock, "plugin.add", "hash", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := rpc.VerifyAttestation(att, rpc.MapTrustStore{att.PubkeyFingerprint: k.pub}, ledger, "plugin.add", "hash", deps.Clock.Now()); err != nil {
		t.Fatal(err)
	}
}
