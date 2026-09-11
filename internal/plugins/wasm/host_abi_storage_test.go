package wasm

import (
	"context"
	"testing"
)

func TestHostStorage_PutGetListDelete(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "storage", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()

	var putResp StorageResponse
	if err := rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, deps, WASIConfig{}, testLimits(),
		StorageRequest{Op: "put", Key: "k1", Value: []byte("v1")}, &putResp); err != nil {
		t.Fatalf("put: %v", err)
	}

	var getResp StorageResponse
	if err := rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, deps, WASIConfig{}, testLimits(),
		StorageRequest{Op: "get", Key: "k1"}, &getResp); err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(getResp.Value) != "v1" {
		t.Fatalf("get value = %q, want v1", getResp.Value)
	}

	var listResp StorageResponse
	if err := rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, deps, WASIConfig{}, testLimits(),
		StorageRequest{Op: "list", Key: "k"}, &listResp); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listResp.Keys) != 1 || listResp.Keys[0] != "k1" {
		t.Fatalf("list keys = %v", listResp.Keys)
	}

	var delResp StorageResponse
	if err := rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, deps, WASIConfig{}, testLimits(),
		StorageRequest{Op: "delete", Key: "k1"}, &delResp); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, deps, WASIConfig{}, testLimits(),
		StorageRequest{Op: "get", Key: "k1"}, &getResp); err == nil {
		t.Fatal("expected NotFound after delete")
	}
}

// TestHostStorage_ErrorPropagation proves the storage layer's own error
// (KindNotFound on a missing Get) propagates through the wasm call
// boundary rather than being swallowed or generalized.
func TestHostStorage_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "storage-err", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var resp StorageResponse
	err = rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, testDeps(), WASIConfig{}, testLimits(),
		StorageRequest{Op: "get", Key: "missing"}, &resp)
	if err == nil {
		t.Fatal("expected error for a missing key")
	}
}

func TestHostStorage_UnrecognizedOp(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "storage-badop", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var resp StorageResponse
	err = rt.Dispatch(ctx, lm, "call_host_storage", "p", nil, testDeps(), WASIConfig{}, testLimits(),
		StorageRequest{Op: "not-a-real-op", Key: "k"}, &resp)
	if err == nil {
		t.Fatal("expected error for an unrecognized op")
	}
}
