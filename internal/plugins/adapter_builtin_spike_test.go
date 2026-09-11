//go:build spike

package plugins

import (
	"context"
	"sync"
)

// builtinAdapter is an in-process HostFn implementation that records
// every call it receives and returns a deterministic fixture response
// per method. It never mutates any state outside its own recorded-calls
// slice (Art.1: test-only, no shared/global state).
type builtinAdapter struct {
	mu    sync.Mutex
	calls []string
}

func newBuiltinAdapter() *builtinAdapter { return &builtinAdapter{} }

func (b *builtinAdapter) record(method string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, method)
}

// boolLen avoids a nil-pointer field read when computing the
// oversized-payload probe against a possibly-nil request.
func boolLen(ok bool, f func() int) int {
	if !ok {
		return 0
	}
	return f()
}

func (b *builtinAdapter) HostHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.URL) })); err != nil {
		return nil, err
	}
	b.record(methodHTTP)
	return &HTTPResponse{Status: 200, Body: "echo:" + req.URL}, nil
}

func (b *builtinAdapter) HostStorage(ctx context.Context, req *StorageRequest) (*StorageResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Value) })); err != nil {
		return nil, err
	}
	b.record(methodStorage)
	v := "stored:" + req.Key
	if req.Op == "set" {
		v = req.Value
	}
	return &StorageResponse{Value: v}, nil
}

func (b *builtinAdapter) HostLog(ctx context.Context, req *LogRequest) (*LogResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Message) })); err != nil {
		return nil, err
	}
	b.record(methodLog)
	return &LogResponse{Accepted: true}, nil
}

func (b *builtinAdapter) HostStream(ctx context.Context, req *StreamRequest) (*StreamResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Data) })); err != nil {
		return nil, err
	}
	b.record(methodStream)
	return &StreamResponse{BytesWritten: len(req.Data)}, nil
}

func (b *builtinAdapter) HostSecretRef(ctx context.Context, req *SecretRefRequest) (*SecretRefResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Name) })); err != nil {
		return nil, err
	}
	b.record(methodSecretRef)
	return &SecretRefResponse{RefID: "ref:" + req.Name}, nil
}

func (b *builtinAdapter) HostEventEmit(ctx context.Context, req *EventEmitRequest) (*EventEmitResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Payload) })); err != nil {
		return nil, err
	}
	b.record(methodEventEmit)
	return &EventEmitResponse{EventID: "evt:" + req.Topic}, nil
}

func (b *builtinAdapter) HostToolRegister(ctx context.Context, req *ToolRegisterRequest) (*ToolRegisterResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Schema) })); err != nil {
		return nil, err
	}
	b.record(methodToolRegister)
	return &ToolRegisterResponse{Registered: true}, nil
}
