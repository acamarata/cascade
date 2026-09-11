package wasm

import (
	"context"
	"encoding/json"

	"github.com/tetratelabs/wazero/api"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the four core host-ABI v1 functions that carry no external
//
//	custody or egress concerns (task 4): host_log, host_stream_emit,
//	host_tool_register, host_event_emit. Each reads a JSON request
//	payload from the guest's own linear memory, delegates to a small
//	local seam (Logger/StreamSink/ToolRegistrar/EventBus), and writes a
//	structured result back — never a panic, bad pointers and invalid
//	lengths both fail closed with a typed error written into the result
//	envelope rather than aborting the guest call.

// Logger is host_log's delegate: routes one structured log line.
type Logger interface {
	Log(ctx context.Context, level, message string) error
}

// EventBus is host_eventemit's delegate: the plugin event bus.
type EventBus interface {
	Emit(ctx context.Context, topic string, payload []byte) (eventID string, err error)
}

// StreamSink is host_stream's delegate: the streaming output channel.
type StreamSink interface {
	Write(ctx context.Context, channelID string, chunk []byte) (bytesWritten int, err error)
}

// ToolRegistrar is host_toolregister's delegate: the host tool registry.
type ToolRegistrar interface {
	Register(ctx context.Context, toolName string, schema []byte) error
}

// readRequest reads length bytes at ptr from m's memory and JSON-decodes
// them into out. A bad pointer/length pair or malformed JSON both fail
// closed with a typed error — never a panic, never a partial decode
// treated as success.
func readRequest(m api.Module, ptr, length uint32, out any) error {
	raw, ok := m.Memory().Read(ptr, length)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "wasm: host-fn request pointer/length out of bounds")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: host-fn request payload is not valid JSON")
	}
	return nil
}

// writeResult marshals res (a result{Payload,Error}) and writes it at
// wasmRespOffset, returning the byte length wazero's calling convention
// expects the host function to return. A marshal or memory-write failure
// still returns a well-formed length-0 response rather than panicking.
func writeResult(m api.Module, res result) uint32 {
	data, err := json.Marshal(res)
	if err != nil {
		data, _ = json.Marshal(result{Error: "wasm: internal: result marshal failed"})
	}
	if !m.Memory().Write(wasmRespOffset, data) {
		return 0
	}
	return uint32(len(data))
}

// writeErr is the common failure path every host-fn handler below uses:
// wrap err as a result{Error} and write it.
func writeErr(m api.Module, err error) uint32 {
	return writeResult(m, result{Error: err.Error()})
}

// writeOK marshals payload and writes it as a result{Payload}.
func writeOK(m api.Module, payload any) uint32 {
	data, err := json.Marshal(payload)
	if err != nil {
		return writeErr(m, cascade.Wrapf(cascade.KindInternal, err, "wasm: response payload marshal failed"))
	}
	return writeResult(m, result{Payload: data})
}

// missingDep is the nil-dependency guard every host-fn handler below
// calls first: a nil Deps field reaching a handler that actually needs
// it is a caller bug (Dispatch invoked without wiring that seam), and
// this reports it as a typed KindInternal result rather than letting the
// handler dereference a nil interface and panic the guest call.
func missingDep(fn string) error {
	return cascade.Newf(cascade.KindInternal, "wasm: %s: dependency not configured", fn)
}

// hostLogFn builds the host_log wazero function bound to cs.
func hostLogFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		if cs.deps.Logger == nil {
			return writeErr(m, missingDep(hostFnLog))
		}
		var req LogRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		if err := cs.deps.Logger.Log(ctx, req.Level, req.Message); err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, LogResponse{Accepted: true})
	}
}

// hostStreamFn builds the host_stream wazero function bound to cs.
func hostStreamFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		if cs.deps.Stream == nil {
			return writeErr(m, missingDep(hostFnStream))
		}
		var req StreamRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		n, err := cs.deps.Stream.Write(ctx, req.ChannelID, req.Data)
		if err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, StreamResponse{BytesWritten: n})
	}
}

// hostToolRegisterFn builds the host_toolregister wazero function bound
// to cs.
func hostToolRegisterFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		if cs.deps.Tools == nil {
			return writeErr(m, missingDep(hostFnToolRegister))
		}
		var req ToolRegisterRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		if err := cs.deps.Tools.Register(ctx, req.ToolName, req.Schema); err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, ToolRegisterResponse{Registered: true})
	}
}

// hostEventEmitFn builds the host_eventemit wazero function bound to cs.
func hostEventEmitFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		if cs.deps.Events == nil {
			return writeErr(m, missingDep(hostFnEventEmit))
		}
		var req EventEmitRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		id, err := cs.deps.Events.Emit(ctx, req.Topic, req.Payload)
		if err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, EventEmitResponse{EventID: id})
	}
}
