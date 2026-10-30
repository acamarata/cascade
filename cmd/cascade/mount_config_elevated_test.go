//go:build integration

package main

// Purpose: P1-SEC-30's AcceptLoosening mount over the real callElevated:
// CASCADE_NO_INPUT refuses before custody opens, and a signed call carries
// the written file's sha256 through the real elevation middleware.

import (
	"context"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestAcceptConfigLooseningNoInputRefusesBeforeKeystore(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	deps.Getenv = func(name string) string {
		if name == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	custodyCalls := 0
	custody := deps.Custody
	deps.Custody = func() elevation.Custody { custodyCalls++; return custody() }
	c, requests := challengeClient(t)
	err := acceptConfigLoosening(t.Context(), func() (*client.Client, error) { return c, nil }, deps, runtime.ConfigDigest([]byte("x")))
	if !errors.Is(err, elevation.ErrNoInput()) || custodyCalls != 0 || k.signs != 0 || requests.Load() != 1 {
		t.Fatalf("err=%v custody=%d signs=%d requests=%d", err, custodyCalls, k.signs, requests.Load())
	}
}

func TestAcceptConfigLooseningSignsTheDigest(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows refuses elevation")
	}
	deps, k := callTestDeps(t, elevation.CustodyPlatform)
	pub, _ := k.PubKeyB64()
	fp, _ := elevation.Fingerprint(pub)
	registry := rpc.NewRegistry()
	registry.Use(rpc.ElevationMiddleware(rpc.ElevationDeps{Ledger: rpc.NewNonceLedger(deps.Clock), Trust: rpc.MapTrustStore{fp: k.pub},
		Clock: deps.Clock, Audit: rpc.LocalCeremonyAudit{}, Custody: func() (string, bool) { return "platform", true }}))
	digest := runtime.ConfigDigest([]byte("[conductor]\nspill_enabled = true\n"))
	var got atomic.Value
	// The real middleware runs the handler only after the attestation over
	// these exact params verified; the handler checks the params decode the
	// way the daemon's accept handler decodes them.
	registry.Register(runtime.AcceptLooseningMethod, func(_ context.Context, p json.RawMessage) (any, error) {
		if d, ok := runtime.DecodeAcceptParams(p); ok {
			got.Store(d)
		}
		return map[string]string{}, nil
	})
	c, _ := callTestClient(t, rpc.NewHandler(registry))
	if err := acceptConfigLoosening(t.Context(), func() (*client.Client, error) { return c, nil }, deps, digest); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got.Load() != digest || k.signs != 1 {
		t.Fatalf("verified config_sha256 = %v (signs %d), want %s", got.Load(), k.signs, digest)
	}
}
