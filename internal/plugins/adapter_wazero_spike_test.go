//go:build spike

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// wasmReqOffset and wasmRespOffset are the two linear-memory scratch
// regions the wazero adapter and test.wasm both agree on: the caller
// writes a request envelope at wasmReqOffset before calling an exported
// wrapper, and the registered host function writes its result envelope
// at wasmRespOffset before returning the response length.
const (
	wasmReqOffset  = uint32(0)     // first 64KiB page: request scratch buffer
	wasmRespOffset = uint32(65536) // second 64KiB page: response scratch buffer
)

// wazeroAdapter loads the real test.wasm module via the real wazero
// library, registers all seven host functions as wazero host functions
// under module name "env", and proxies each through wasm linear memory
// to an internal builtin-shaped HostFn implementation. Every typed
// method call this spike makes actually crosses a real wasm call
// boundary (Art.2): Go writes the request into module memory, calls the
// wasm-exported wrapper, which calls the registered host import, which
// writes the response back into module memory for Go to read.
type wazeroAdapter struct {
	runtime  wazero.Runtime
	mod      api.Module
	delegate *builtinAdapter
}

func newWazeroAdapter(ctx context.Context, t *testing.T) *wazeroAdapter {
	t.Helper()
	wasmPath := filepath.Join("testdata", "conformance", "test.wasm")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read %s: %v", wasmPath, err)
	}

	r := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = r.Close(ctx) })

	w := &wazeroAdapter{runtime: r, delegate: newBuiltinAdapter()}
	if err := w.registerHostFunctions(ctx); err != nil {
		t.Fatalf("register host module: %v", err)
	}

	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("compile test.wasm: %v", err)
	}
	mod, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		t.Fatalf("instantiate test.wasm: %v", err)
	}
	w.mod = mod
	return w
}

func (w *wazeroAdapter) registerHostFunctions(ctx context.Context) error {
	builder := w.runtime.NewHostModuleBuilder("env")
	for _, hf := range []struct {
		name   string
		method string
	}{
		{"host_http", methodHTTP},
		{"host_storage", methodStorage},
		{"host_log", methodLog},
		{"host_stream", methodStream},
		{"host_secretref", methodSecretRef},
		{"host_eventemit", methodEventEmit},
		{"host_toolregister", methodToolRegister},
	} {
		builder = builder.NewFunctionBuilder().WithFunc(w.hostCallback(hf.method)).Export(hf.name)
	}
	_, err := builder.Instantiate(ctx)
	return err
}

// hostCallback builds the wazero host function for one ABI method: it
// reads the request envelope from the caller's memory, dispatches to
// w.delegate's typed method, writes a result envelope back at
// wasmRespOffset, and returns the response length.
func (w *wazeroAdapter) hostCallback(method string) func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		reqBytes, ok := m.Memory().Read(ptr, length)
		if !ok {
			return writeErrorResult(m, "read request memory failed")
		}
		var env envelope
		if err := json.Unmarshal(reqBytes, &env); err != nil {
			return writeErrorResult(m, "decode envelope: "+err.Error())
		}
		payload, err := w.delegate.dispatchRaw(ctx, method, env.Payload)
		if err != nil {
			return writeErrorResult(m, err.Error())
		}
		return writeOkResult(m, payload)
	}
}

func writeErrorResult(m api.Module, msg string) uint32 {
	data, _ := json.Marshal(result{Error: msg})
	m.Memory().Write(wasmRespOffset, data)
	return uint32(len(data))
}

func writeOkResult(m api.Module, payload json.RawMessage) uint32 {
	data, _ := json.Marshal(result{Payload: payload})
	m.Memory().Write(wasmRespOffset, data)
	return uint32(len(data))
}

// callWasm is the shared plumbing every wazeroAdapter typed method uses:
// write reqData at wasmReqOffset, call the exported wasm wrapper, then
// read and decode the response envelope.
func (w *wazeroAdapter) callWasm(ctx context.Context, exportName, method string, req any) (json.RawMessage, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	env := envelope{Method: method, Payload: payload}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if !w.mod.Memory().Write(wasmReqOffset, data) {
		return nil, fmt.Errorf("write request memory failed")
	}
	fn := w.mod.ExportedFunction(exportName)
	if fn == nil {
		return nil, fmt.Errorf("exported function %q not found", exportName)
	}
	results, err := fn.Call(ctx, uint64(wasmReqOffset), uint64(len(data)))
	if err != nil {
		return nil, err
	}
	respLen := uint32(results[0])
	respBytes, ok := w.mod.Memory().Read(wasmRespOffset, respLen)
	if !ok {
		return nil, fmt.Errorf("read response memory failed")
	}
	var res result
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, fmt.Errorf("%s", res.Error)
	}
	return res.Payload, nil
}
