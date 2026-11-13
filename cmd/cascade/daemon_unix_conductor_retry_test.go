//go:build !windows

// Purpose: production reach of transient retry (P1-GWY-03): the Executor
// the daemon composition builds, over the router options it applies,
// retries a transient upstream failure; a 401 is never retried. Removing
// daemonRetryOption from conductorRouterOptions turns this red.
// Inputs: a per-test canary printed by a helper under t.TempDir().
// Outputs: none.
// Constraints: no network, no vault, no keychain; HOME is a temp dir.
// SPORT: cmd/cascade daemon (CHANGE, P1-GWY-03).

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
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
)

// scriptedTransport answers each send with the next status; the last one
// repeats.
type scriptedTransport struct {
	mu       sync.Mutex
	statuses []int
	sends    int
}

func (s *scriptedTransport) Send(context.Context, string, string, map[string]string, []byte) (int, map[string][]string, io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statuses[min(s.sends, len(s.statuses)-1)]
	s.sends++
	body := gatewayTestOK
	if status != 200 {
		body = `{"type":"error","error":{"type":"api_error","message":"failure"}}`
	}
	return status, nil, io.NopCloser(strings.NewReader(body)), nil
}

// retryDaemonExecute dispatches one conductor.execute through the daemon's
// composition with the production router options.
func retryDaemonExecute(t *testing.T, cred map[string]interface{}, tr *scriptedTransport) string {
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
	cfg := admittedGatewayConfig(cred, true)
	resolver, err := newDaemonResolver(reg, &gatewayVault{}, clock, tr, cfg)
	if err != nil {
		t.Fatalf("newDaemonResolver: %v", err)
	}
	security, err := conductorSecurity(paths)
	if err != nil {
		t.Fatalf("conductorSecurity: %v", err)
	}
	policy, err := buildConductorPolicy(func() *runtime.Config { return cfg }, clock)
	if err != nil {
		t.Fatalf("buildConductorPolicy: %v", err)
	}
	security.Policy = conductor.NewOwnerPolicy(policy.Deny)
	store := storetest.NewMemStore()
	rpcReg := rpc.NewRegistry()
	if err := daemon.RegisterConductorExecuteHandler(rpcReg, daemon.NewManifest(nil, clock), providerregistry.NewReader(reg),
		policy.Quota, resolver, audit.New(store, clock, nil), clock, security, daemon.ConductorAccounting{Reserver: passReserver{}},
		daemon.NewConductorFanOut(store, clock), conductorRouterOptions(nil, paths, clock, nil)...); err != nil {
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

func TestDaemonExecutorHasRetry(t *testing.T) {
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

	flaky := &scriptedTransport{statuses: []int{503, 200}}
	out := retryDaemonExecute(t, cred, flaky)
	if flaky.sends != 2 || strings.HasPrefix(out, "error: ") {
		t.Fatalf("execute = %s after %d sends; want a 503 retried once on the same lane, then success", out, flaky.sends)
	}
	if strings.Contains(out, canary) {
		t.Fatal("canary found in the result")
	}

	unauthorized := &scriptedTransport{statuses: []int{401}}
	out = retryDaemonExecute(t, cred, unauthorized)
	if unauthorized.sends != 1 || !strings.HasPrefix(out, "error: ") {
		t.Fatalf("execute = %s after %d sends; want a 401 sent once and refused", out, unauthorized.sends)
	}
}
