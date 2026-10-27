//go:build !windows

// Purpose: fixtures for the elevation wiring tests: an injected in-memory
// custody, a daemon registry over a store the tests can read, a composed
// daemon, and a caller that answers a challenge by signing.
// Outputs: newElevatedTestRegistry, the helper later elevated-RPC tests reuse.
// Constraints: every fixture restores the selector it replaced.
// SPORT: cmd/cascade elevation wiring tests (P1-SEC-03).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// elevatedTestPaths is a daemon path provider whose data directory is chosen
// by the test.
type elevatedTestPaths struct {
	fakeDaemonPaths
	data string
}

func (p elevatedTestPaths) DataDir() string { return p.data }

// useElevationCustody makes the daemon's elevation wiring select ks as a
// platform-tier custody for the test, and returns ks's public key base64.
func useElevationCustody(t *testing.T, ks signingKeystore) string {
	t.Helper()
	previous := daemonElevationSelector
	daemonElevationSelector = func(dataDir string) elevation.Selector {
		return elevation.Selector{DataDir: dataDir, Sources: []elevation.CustodySource{{
			Tier: elevation.CustodyPlatform, Name: "test",
			Open: func(string) (elevation.ElevationKeystore, bool) { return ks, true },
		}}}
	}
	t.Cleanup(func() { daemonElevationSelector = previous })
	pub, _ := ks.PubKeyB64()
	return pub
}

// enrollKey records pubB64 as the trusted key under dataDir.
func enrollKey(t *testing.T, dataDir, pubB64 string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := elevation.NewElevationTrustStore(elevation.NewFileBackend(dataDir), runtime.NewSystemClock()).Enroll(pubB64); err != nil {
		t.Fatalf("enrol: %v", err)
	}
}

// elevatedCaller dials method on reg the way callElevated does: unsigned
// first, then one signed retry when the server answers with a nonce.
func elevatedCaller(reg *rpc.Registry, ks elevation.ElevationKeystore, clock runtime.Clock) func(ctx context.Context, method string, params, out any) error {
	return func(ctx context.Context, method string, params, out any) error {
		ctx = principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
		p, err := json.Marshal(params)
		if err != nil {
			return err
		}
		res, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: p})
		if eo != nil && eo.Code == cascade.RPCCodeElevationRequired {
			nonce := nonceOf(eo)
			if nonce == "" {
				return eo
			}
			att, err := signAttestation(ks, clock, method, rpc.ParamsHash(p), nonce)
			if err != nil {
				return err
			}
			env, err := json.Marshal(map[string]any{"_attestation": att, "_args": json.RawMessage(p)})
			if err != nil {
				return err
			}
			res, eo = reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: env})
		}
		if eo != nil {
			return eo
		}
		if out == nil {
			return nil
		}
		raw, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
}

func nonceOf(eo *rpc.ErrorObject) string {
	raw, _ := json.Marshal(eo.Data)
	var d struct{ Nonce string }
	_ = json.Unmarshal(raw, &d)
	return d.Nonce
}

// elevatedDaemon is the daemon registry over an in-memory store with an
// injected custody: the light fixture (no composeDaemon) for tests that need
// to read the store the handlers write.
type elevatedDaemon struct {
	Registry *rpc.Registry
	Store    provider.Store
	KS       signingKeystore
	DataDir  string
	Call     func(ctx context.Context, method string, params, out any) error
}

// newElevatedDaemon builds the daemon registry over a fresh in-memory store
// with a platform-tier test custody; enrol decides whether the custody's key
// is enrolled before the registry is built (trust is bound at build time).
func newElevatedDaemon(t *testing.T, dataDir string, enrol bool) elevatedDaemon {
	t.Helper()
	ks := newSigningKeystore(t)
	pub := useElevationCustody(t, ks)
	if enrol {
		enrollKey(t, dataDir, pub)
	}
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	paths := elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: filepath.Dir(dataDir)}, data: dataDir}
	reg, _, _, _, err := buildDaemonRegistry(events.New(store, clock), clock, nil, daemon.Settings{}, paths, nil, store)
	if err != nil {
		t.Fatalf("buildDaemonRegistry: %v", err)
	}
	return elevatedDaemon{Registry: reg, Store: store, KS: ks, DataDir: dataDir, Call: elevatedCaller(reg, ks, clock)}
}

// newElevatedTestRegistry builds the daemon registry over a fresh store with
// an injected platform-tier test custody, enrols that custody's key under
// dataDir, and returns the registry with a call function that answers the
// server's challenge by signing (the shape callElevated has). For tests of
// later elevated RPCs (P1-PLG and others) that need a registry whose elevation
// answers a real attestation without a host keystore.
func newElevatedTestRegistry(t *testing.T, dataDir string) (*rpc.Registry, func(ctx context.Context, method string, params, out any) error) {
	t.Helper()
	d := newElevatedDaemon(t, dataDir, true)
	return d.Registry, d.Call
}

// composedElevated is a composed daemon with an injected custody.
type composedElevated struct {
	composedForTest
	Deps daemonDeps
	KS   signingKeystore
	Call func(ctx context.Context, method string, params, out any) error
}

// composeElevated runs composeDaemon over a fresh HOME with the injected
// custody; enrol decides whether the custody's key is enrolled before start.
func composeElevated(t *testing.T, enrol bool) composedElevated {
	t.Helper()
	ks := newSigningKeystore(t) // sets HOME, USERPROFILE and CASCADE_HOME to one fresh dir
	pub := useElevationCustody(t, ks)
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	if enrol {
		enrollKey(t, deps.Paths.DataDir(), pub)
	}
	var got composedForTest
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, m *rpc.SSEMux) { got.Registry, got.Events = r, m })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	got.Opts = opts
	return composedElevated{composedForTest: got, Deps: deps, KS: ks, Call: elevatedCaller(got.Registry, ks, runtime.NewSystemClock())}
}
