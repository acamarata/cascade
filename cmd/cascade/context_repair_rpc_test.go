//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBuildRPCServerRegistersContextRepair(t *testing.T) {
	factsTestEnv(t)
	reg := composeForTest(t).Registry
	reg.SetAccessPolicy(nil)
	for _, tc := range []struct {
		caller principal.Caller
		kind   cascade.Kind
	}{
		{principal.Caller{OwnerCLI: true}, cascade.KindInvalidInput},
		{principal.Caller{Principal: &principal.Principal{ID: "session", Kind: principal.KindSession, Scope: "project:x"}}, cascade.KindPermissionDenied},
	} {
		ctx := principal.WithCaller(t.Context(), tc.caller)
		_, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "context.repair", Params: json.RawMessage(`{}`)})
		if eo == nil || eo.Code != cascade.NewRPCError(cascade.New(tc.kind, "x")).Code {
			t.Fatalf("caller %+v: got %+v, want %s", tc.caller, eo, tc.kind)
		}
	}
	t.Run("no_database", testRepairWithoutDatabase)
}

func testRepairWithoutDatabase(t *testing.T) {
	factsTestEnv(t)
	d := newElevatedDaemon(t, t.TempDir(), false)
	ctx := principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})
	raw, _ := json.Marshal(repairParams{Repo: "/this-path-must-never-be-registered"})
	_, eo := d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: contextRepairMethod, Params: raw})
	if eo == nil || eo.Code != cascade.NewRPCError(cascade.New(cascade.KindUnavailable, "x")).Code ||
		!strings.Contains(eo.Message, "repository watcher") {
		t.Fatalf("no database: %+v", eo)
	}
}

func TestContextRepairRebuildAuditKind(t *testing.T) {
	r := composeRepoWatch(t)
	root, id := repairFixture(t, r)
	before := r.pathsFor(t, id)
	for _, tc := range []struct {
		params  repairParams
		args    []string
		message string
	}{
		{repairParams{Repo: root, Rebuild: true}, []string{"--rebuild"}, "requires a reason of 1..200 runes"},
		{repairParams{Repo: root, Rebuild: true, Reason: strings.Repeat("界", 201)},
			[]string{"--rebuild", "--reason", strings.Repeat("界", 201)}, "requires a reason of 1..200 runes"},
		{repairParams{Repo: root, Reason: "x"}, []string{"--reason", "x"}, "--reason requires --rebuild"},
	} {
		assertRepairCLIInvalid(t, r, root, tc.args, tc.message)
		raw, _ := json.Marshal(tc.params)
		ctx := principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})
		_, eo := r.registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: contextRepairMethod, Params: raw})
		if eo == nil || eo.Code != cascade.NewRPCError(cascade.New(cascade.KindInvalidInput, "x")).Code ||
			!strings.Contains(eo.Message, tc.message) {
			t.Fatalf("invalid RPC: %+v", eo)
		}
	}
	for _, raw := range []string{`{"repo":"relative"}`, `null`, `{}`,
		`{"repo":"/","unknown":true}`, `{"repo":"/","rebuild":"yes"}`, `{} {}`} {
		ctx := principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})
		_, eo := r.registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: contextRepairMethod, Params: json.RawMessage(raw)})
		if eo == nil || eo.Code != cascade.NewRPCError(cascade.New(cascade.KindInvalidInput, "x")).Code {
			t.Fatalf("%s: %+v", raw, eo)
		}
	}
	if len(r.auditRows(t)) != 0 || !reflect.DeepEqual(before, r.pathsFor(t, id)) {
		t.Fatal("invalid input mutated audit or journal")
	}
}

func assertRepairCLIInvalid(t *testing.T, r *repairRig, root string, args []string, message string) {
	t.Helper()
	calls := 0
	cmd := newContextCmd(contextScopeDeps{Paths: r.paths, DialContext: countRepairDials(client.UnixDialer, &calls)})
	cmd.SetArgs(append([]string{"repair", "--repo", root}, args...))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	err := cmd.ExecuteContext(context.Background())
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), message) || calls != 0 {
		t.Fatalf("CLI input: %v; dials %d", err, calls)
	}
}
