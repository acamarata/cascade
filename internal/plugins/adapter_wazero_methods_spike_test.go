//go:build spike

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
)

// dispatchRaw lets the wazero host callback reach the builtin adapter's
// typed logic through the same generic envelope shape the process
// adapter uses, without re-checking the common preconditions a second
// time (the wazero adapter's own typed methods already checked them
// before crossing into wasm).
func (b *builtinAdapter) dispatchRaw(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	switch method {
	case methodHTTP:
		return dispatchOne(payload, func(req *HTTPRequest) (any, error) { return b.HostHTTP(ctx, req) })
	case methodStorage:
		return dispatchOne(payload, func(req *StorageRequest) (any, error) { return b.HostStorage(ctx, req) })
	case methodLog:
		return dispatchOne(payload, func(req *LogRequest) (any, error) { return b.HostLog(ctx, req) })
	case methodStream:
		return dispatchOne(payload, func(req *StreamRequest) (any, error) { return b.HostStream(ctx, req) })
	case methodSecretRef:
		return dispatchOne(payload, func(req *SecretRefRequest) (any, error) { return b.HostSecretRef(ctx, req) })
	case methodEventEmit:
		return dispatchOne(payload, func(req *EventEmitRequest) (any, error) { return b.HostEventEmit(ctx, req) })
	case methodToolRegister:
		return dispatchOne(payload, func(req *ToolRegisterRequest) (any, error) { return b.HostToolRegister(ctx, req) })
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

// dispatchOne decodes payload into a *Req, invokes call, and marshals the
// resulting response. Generic over the request type so each method case
// above stays a one-liner.
func dispatchOne[Req any](payload json.RawMessage, call func(*Req) (any, error)) (json.RawMessage, error) {
	var req Req
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	resp, err := call(&req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}

func (w *wazeroAdapter) HostHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.URL) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_http", methodHTTP, req)
	if err != nil {
		return nil, err
	}
	var resp HTTPResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostStorage(ctx context.Context, req *StorageRequest) (*StorageResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Value) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_storage", methodStorage, req)
	if err != nil {
		return nil, err
	}
	var resp StorageResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostLog(ctx context.Context, req *LogRequest) (*LogResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Message) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_log", methodLog, req)
	if err != nil {
		return nil, err
	}
	var resp LogResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostStream(ctx context.Context, req *StreamRequest) (*StreamResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Data) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_stream", methodStream, req)
	if err != nil {
		return nil, err
	}
	var resp StreamResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostSecretRef(ctx context.Context, req *SecretRefRequest) (*SecretRefResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Name) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_secretref", methodSecretRef, req)
	if err != nil {
		return nil, err
	}
	var resp SecretRefResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostEventEmit(ctx context.Context, req *EventEmitRequest) (*EventEmitResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Payload) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_eventemit", methodEventEmit, req)
	if err != nil {
		return nil, err
	}
	var resp EventEmitResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (w *wazeroAdapter) HostToolRegister(ctx context.Context, req *ToolRegisterRequest) (*ToolRegisterResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Schema) })); err != nil {
		return nil, err
	}
	payload, err := w.callWasm(ctx, "call_host_toolregister", methodToolRegister, req)
	if err != nil {
		return nil, err
	}
	var resp ToolRegisterResponse
	return &resp, json.Unmarshal(payload, &resp)
}
