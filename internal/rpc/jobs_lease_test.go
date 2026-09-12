package rpc

// Purpose: lease.list/lease.release table-driven tests, split from
//	jobs_test.go purely to keep both files under the 300-line cap
//	(Art.10.3) -- same fakes (fakeLeaseStore, testJobDeps, dispatchJSON,
//	ed25519GenKey), same coverage concern as that file's own header.
//
// SPORT: rpc/lease.* methods (ADD) (P1-E29-W6-S60-T1).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestLeaseList_ReturnsAllLeases(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	leases := newFakeLeaseStore(LeaseRecord{ID: "r:a", Holder: "j1", State: "held"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(newFakeJobStore(), leases, allowAllGuard{}, clock))

	raw, errObj := dispatchJSON(t, registry, "lease.list", map[string]any{})
	if errObj != nil {
		t.Fatalf("lease.list: %v", errObj)
	}
	var page LeasePage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Leases) != 1 || page.Leases[0].ID != "r:a" {
		t.Fatalf("lease.list = %+v", page.Leases)
	}
}

func TestLeaseRelease_OwnLeaseUnelevated(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	leases := newFakeLeaseStore(LeaseRecord{ID: "r:a", Holder: "j1", State: "held", Epoch: 1})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(newFakeJobStore(), leases, allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "lease.release", map[string]any{"id": "r:a", "as_job": "j1"})
	if errObj != nil {
		t.Fatalf("release of own lease must not be elevated: %v", errObj)
	}
	if leases.leases["r:a"].State != "released" {
		t.Fatalf("lease state = %q, want released", leases.leases["r:a"].State)
	}
}

func TestLeaseRelease_OtherHolderRequiresElevation(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	leases := newFakeLeaseStore(LeaseRecord{ID: "r:a", Holder: "job-owner", State: "held", Epoch: 1})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(newFakeJobStore(), leases, allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "lease.release", map[string]any{"id": "r:a", "as_job": "someone-else"})
	if errObj == nil {
		t.Fatal("expected ELEVATION_REQUIRED for releasing another job's lease")
	}
	if errObj.Code != codeElevationRequired {
		t.Fatalf("code = %d, want %d (ELEVATION_REQUIRED)", errObj.Code, codeElevationRequired)
	}
}

func TestLeaseRelease_ValidAttestationSatisfiesElevation(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	leases := newFakeLeaseStore(LeaseRecord{ID: "r:a", Holder: "job-owner", State: "held", Epoch: 1})
	registry := NewRegistry()
	deps := testJobDeps(newFakeJobStore(), leases, allowAllGuard{}, clock)
	pub, priv := ed25519GenKey(t)
	deps.Trust = MapTrustStore{"fp1": pub}
	RegisterJobHandlers(registry, deps)

	args, _ := json.Marshal(leaseReleaseParams{ID: "r:a", AsJob: "someone-else"})
	hash := hashParams(args)
	nonce, err := deps.Ledger.Issue("lease.release", hash)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	att := signAttestation(priv, Attestation{
		RequestID: "r1", ActionHash: hash, Nonce: nonce, PubkeyFingerprint: "fp1",
		IssuedUnix: clock.Now().Unix(), ExpUnix: clock.Now().Add(time.Minute).Unix(),
	})
	envelope, _ := json.Marshal(elevatedEnvelope{Attestation: &att, Args: args})

	result, errObj := registry.Dispatch(context.Background(), &Request{Method: "lease.release", Params: envelope})
	if errObj != nil {
		t.Fatalf("lease.release with valid attestation: %v", errObj)
	}
	_ = result
	if leases.leases["r:a"].State != "released" {
		t.Fatalf("lease state = %q, want released", leases.leases["r:a"].State)
	}
}

func TestLeaseRelease_ReplayedAttestationRefused(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	leases := newFakeLeaseStore(LeaseRecord{ID: "r:a", Holder: "job-owner", State: "held", Epoch: 1})
	registry := NewRegistry()
	deps := testJobDeps(newFakeJobStore(), leases, allowAllGuard{}, clock)
	pub, priv := ed25519GenKey(t)
	deps.Trust = MapTrustStore{"fp1": pub}
	RegisterJobHandlers(registry, deps)

	args, _ := json.Marshal(leaseReleaseParams{ID: "r:a", AsJob: "someone-else"})
	hash := hashParams(args)
	nonce, _ := deps.Ledger.Issue("lease.release", hash)
	att := signAttestation(priv, Attestation{
		RequestID: "r1", ActionHash: hash, Nonce: nonce, PubkeyFingerprint: "fp1",
		IssuedUnix: clock.Now().Unix(), ExpUnix: clock.Now().Add(time.Minute).Unix(),
	})
	envelope, _ := json.Marshal(elevatedEnvelope{Attestation: &att, Args: args})

	if _, errObj := registry.Dispatch(context.Background(), &Request{Method: "lease.release", Params: envelope}); errObj != nil {
		t.Fatalf("first release: %v", errObj)
	}
	// Second lease at the same fence, forcing a second release attempt
	// with the SAME (already-consumed) nonce.
	leases.leases["r:a"] = LeaseRecord{ID: "r:a", Holder: "job-owner", State: "held", Epoch: 1}
	_, errObj := registry.Dispatch(context.Background(), &Request{Method: "lease.release", Params: envelope})
	if errObj == nil {
		t.Fatal("expected a typed error for a replayed single-use nonce")
	}
}

func ed25519GenKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}
