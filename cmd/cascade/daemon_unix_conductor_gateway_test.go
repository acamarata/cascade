//go:build !windows

// Purpose: production reach of gateway dispatch (P1-GWY-08): a gateway
// provider in a real providers.db, an account admitted through the
// guarded-config seam (EffectiveConfigOf after ApplyEffective), a fake
// credential helper and a fake transport; one conductor.execute through
// the real Executor, over the resolver newDaemonResolver builds, reaches
// the transport with the helper's bearer token. The same config written
// back at the restrictive defaults (not admitted) sends nothing.
// Inputs: a per-test canary printed by a helper under t.TempDir().
// Outputs: none.
// Constraints: no network, no vault, no keychain; HOME is a temp dir.
// SPORT: cmd/cascade daemon (CHANGE, P1-GWY-08).

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// passReserver is the cmd/cascade test reserver: every dispatch gets a hold
// that runs fn and releases nothing. Production has no such default.
type passReserver struct{}

func (passReserver) Reserve(context.Context, provider.Selection, provider.ModelRequest) (conductor.HeldCall, error) {
	return passHold{id: "pass"}, nil
}

func (passReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (conductor.HeldCall, error) {
	return passHold{id: id}, nil
}

type passHold struct{ id string }

func (h passHold) ID() string { return h.id }

func (passHold) WithPermit(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func (passHold) Release(context.Context, provider.Usage, bool) error { return nil }

const (
	gatewayTestBase = "https://gateway.invalid"
	gatewayTestOK   = `{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
)

// gatewayFakeTransport records each request's URL and headers.
type gatewayFakeTransport struct {
	mu      sync.Mutex
	urls    []string
	headers []map[string]string
}

func (g *gatewayFakeTransport) Send(_ context.Context, _, url string, headers map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.urls, g.headers = append(g.urls, url), append(g.headers, headers)
	return 200, nil, io.NopCloser(strings.NewReader(gatewayTestOK)), nil
}

// gatewayVault is the standing-grant reader stand-in; a gateway ref must
// never reach it.
type gatewayVault struct{ n int }

func (v *gatewayVault) Resolve(context.Context, string) (string, error) {
	v.n++
	return "", cascade.New(cascade.KindUnavailable, "test: no grant")
}

// recordingStore keeps a copy of every value written, for the canary scan.
type recordingStore struct {
	*storetest.MemStore
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *recordingStore) Put(ctx context.Context, ns, key string, value []byte) error {
	s.mu.Lock()
	s.buf.Write(value)
	s.mu.Unlock()
	return s.MemStore.Put(ctx, ns, key, value)
}

func (s *recordingStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.MemStore.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, recordingTx{Tx: tx, s: s})
	})
}

type recordingTx struct {
	provider.Tx
	s *recordingStore
}

func (t recordingTx) Put(ctx context.Context, ns, key string, value []byte) error {
	t.s.mu.Lock()
	t.s.buf.Write(value)
	t.s.mu.Unlock()
	return t.Tx.Put(ctx, ns, key, value)
}

// gatewayHelper writes a helper printing canary in two halves (the file
// never holds it whole) and returns its path and digest.
func gatewayHelper(t *testing.T, dir, canary string) (string, string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nprintf '%%s\\n' '%s'\n", canary[:10], canary[10:])
	p := filepath.Join(dir, "fake-helper")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return p, hex.EncodeToString(sum[:])
}

// gatewayCredential is the account's credential keys: the helper, or, where
// this environment forbids exec of a temp-dir script (some sandboxes do),
// an environment variable holding canary.
func gatewayCredential(t *testing.T, helper, sum, canary string) map[string]interface{} {
	t.Helper()
	var ee *exec.ExitError
	if err := exec.Command(helper).Run(); err != nil && !errors.As(err, &ee) { //nolint:gosec // test-written script
		t.Logf("cannot exec a temp-dir script here (%v); the env credential source stands in", err)
		t.Setenv("GW_TEST_TOKEN", canary)
		return map[string]interface{}{"key_env": "GW_TEST_TOKEN"}
	}
	return map[string]interface{}{"credential_helper": helper, "helper_sha256": sum}
}

// admittedGatewayConfig returns a config holding account "acct" written back
// through ApplyEffective: at its own values (admitted) or at the restrictive
// defaults (an unaccepted loosening).
func admittedGatewayConfig(cred map[string]interface{}, admitted bool) *runtime.Config {
	acct := map[string]interface{}{"base_url": gatewayTestBase, "models": []interface{}{"model-a"}}
	for k, v := range cred {
		acct[k] = v
	}
	cfg := &runtime.Config{Extra: map[string]interface{}{"gateways": map[string]interface{}{"acct": acct}}}
	eff := runtime.MostRestrictiveDefaults()
	if admitted {
		eff = runtime.EffectiveConfigOf(cfg)
	}
	runtime.ApplyEffective(cfg, eff)
	return cfg
}

// seedGatewayProvider stores provider gw1 on account acct and its lane.
func seedGatewayProvider(t *testing.T, reg *providerregistry.Registry) {
	t.Helper()
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, providerregistry.ProviderRecord{Name: "gw1", Driver: providerregistry.DriverAnthropic,
		BaseURL: gatewayTestBase, Auth: providerregistry.AuthGateway, AuthRef: "gateway:acct", KnownModels: []string{"model-a"},
		AccountKind: providerregistry.AccountPersonal, Tier: providerregistry.TierMid, HealthStatus: providerregistry.HealthUnknown}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := reg.UpsertLane(ctx, providerregistry.LaneRecord{LaneName: "gw1", ProviderName: "gw1", Weight: 1,
		Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
}

// gatewayExecute registers conductor.execute over the daemon resolver for
// cfg and dispatches one chat; it returns the result or error text.
func gatewayExecute(t *testing.T, cfg *runtime.Config, tr *gatewayFakeTransport, vault *gatewayVault, store *recordingStore) string {
	t.Helper()
	resetProcessFirewall(t)
	paths := fakeMemoryPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewSystemClock()
	reg, closeReg := openLaneRegistry(t, paths.DataDir(), clock)
	t.Cleanup(closeReg)
	seedGatewayProvider(t, reg)
	resolver, err := newDaemonResolver(reg, vault, clock, tr, cfg)
	if err != nil {
		t.Fatalf("newDaemonResolver: %v", err)
	}
	security, err := conductorSecurity(paths)
	if err != nil {
		t.Fatalf("conductorSecurity: %v", err)
	}
	// The owner deny store over the same admitted config, as attach builds it.
	policy, err := buildConductorPolicy(func() *runtime.Config { return cfg }, clock)
	if err != nil {
		t.Fatalf("buildConductorPolicy: %v", err)
	}
	security.Policy = conductor.NewOwnerPolicy(policy.Deny)
	rpcReg := rpc.NewRegistry()
	if err := daemon.RegisterConductorExecuteHandler(rpcReg, daemon.NewManifest(nil, clock), providerregistry.NewReader(reg),
		policy.Quota, resolver, audit.New(store, clock, nil), clock,
		security, daemon.ConductorAccounting{Reserver: passReserver{}}, daemon.NewConductorFanOut(store, clock)); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: %v", err)
	}
	params, _ := json.Marshal(map[string]any{"task_id": "t1", "task_class": "chat", "sensitivity": "public",
		"inputs": []map[string]string{{"role": "user", "content": "hello"}}})
	res, errObj := rpcReg.Dispatch(context.Background(), &rpc.Request{Method: daemon.ConductorExecuteMethod, Params: params})
	if errObj != nil {
		return "error: " + errObj.Message
	}
	out, _ := json.Marshal(res)
	return string(out)
}

func TestDaemonConductorResolvesGatewayProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	canary := hex.EncodeToString(b[:])
	helper, sum := gatewayHelper(t, home, canary)
	cred := gatewayCredential(t, helper, sum, canary)

	tr, vault, store := &gatewayFakeTransport{}, &gatewayVault{}, &recordingStore{MemStore: storetest.NewMemStore()}
	out := gatewayExecute(t, admittedGatewayConfig(cred, true), tr, vault, store)
	if len(tr.urls) != 1 || !strings.HasPrefix(tr.urls[0], gatewayTestBase+"/") {
		t.Fatalf("execute = %s; sends %v, want one to the admitted account base URL", out, tr.urls)
	}
	if tr.headers[0]["authorization"] != "Bearer "+canary || vault.n != 0 {
		t.Fatalf("request lacked the helper's bearer token, or the vault was read %d times", vault.n)
	}
	if strings.HasPrefix(out, "error: ") {
		t.Fatalf("execute failed after the send: %s", out)
	}
	if strings.Contains(out, canary) || strings.Contains(store.buf.String(), canary) {
		t.Fatal("canary found in the result or in a stored audit, usage or event record")
	}

	notAdmitted := &gatewayFakeTransport{}
	out = gatewayExecute(t, admittedGatewayConfig(cred, false), notAdmitted, vault, store)
	if len(notAdmitted.urls) != 0 || !strings.HasPrefix(out, "error: ") || strings.Contains(out, canary) {
		t.Fatalf("unadmitted account: execute = %s after %d sends, want a refusal and no send", out, len(notAdmitted.urls))
	}
}
