package wasm

import (
	"context"

	"github.com/tetratelabs/wazero/api"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: host_storage (task 5), the single wazero-level function
//
//	multiplexing StorageRequest.Op across get/put/delete/list, delegating
//	directly to pkg/plugin.Storage (the plugin ABI's own storage
//	interface — a pkg/ type, so this file may import it directly despite
//	the plugins-providers-boundary rule; internal/storage.PluginStorage
//	is the real implementation a composition root binds in as this
//	interface, per that package's own var _ plugin.Storage assertion).
//	An unrecognized Op fails closed with a typed invalid-input error;
//	the underlying Storage's own error (including its sensitive-payload
//	refusal on Set) propagates through unit-test-verified per the task's
//	"storage-layer error propagation" requirement.

// hostStorageFn builds the host_storage wazero function bound to cs.
func hostStorageFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		var req StorageRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		if cs.deps.Storage == nil {
			return writeErr(m, missingDep(hostFnStorage))
		}
		resp, err := dispatchStorage(ctx, cs.deps.Storage, req)
		if err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, resp)
	}
}

// dispatchStorage routes one StorageRequest to the matching
// plugin.Storage method. Every branch returns the storage layer's own
// error unchanged when it fails, so a caller sees the real cause (e.g.
// KindNotFound on a missing Get key, the sensitive-payload refusal on
// Set) rather than a generic wrapper.
func dispatchStorage(ctx context.Context, store plugin.Storage, req StorageRequest) (StorageResponse, error) {
	switch req.Op {
	case "get":
		v, err := store.Get(ctx, req.Key)
		if err != nil {
			return StorageResponse{}, err
		}
		return StorageResponse{Value: v}, nil
	case "put":
		if err := store.Set(ctx, req.Key, req.Value); err != nil {
			return StorageResponse{}, err
		}
		return StorageResponse{}, nil
	case "delete":
		if err := store.Delete(ctx, req.Key); err != nil {
			return StorageResponse{}, err
		}
		return StorageResponse{}, nil
	case "list":
		keys, err := store.List(ctx, req.Key)
		if err != nil {
			return StorageResponse{}, err
		}
		return StorageResponse{Keys: keys}, nil
	default:
		return StorageResponse{}, cascade.Newf(cascade.KindInvalidInput, "wasm: host_storage: unrecognized op %q", req.Op)
	}
}
