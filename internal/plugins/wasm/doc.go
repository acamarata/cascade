// Package wasm implements the pure-Go WASM plugin runtime: wazero module
// loading, a WASI snapshot_preview1 subset, per-instance resource limits,
// and the versioned host-ABI v1 surface guest modules call into.
//
// Purpose: the wazero-backed sibling of internal/plugins/process, one of
//
//	the runtimes internal/plugins.BuiltinRegistry's dispatch layer can
//	route a plugin call through (per the O/S-30.T6 host-ABI parity spike,
//	all three runtimes speak the same seven-function surface).
//
// Inputs: a compiled WASM module's bytes (Load) and, per call, a plugin
//
//	id, declared net scopes, and the host-side Deps a guest's host-fn
//	calls delegate to (storage, secrets, network, logging, events,
//	streaming, tool registration).
//
// Outputs: a LoadedModule (cached, re-instantiated per call for guest
//
//	isolation) and Dispatch's structured result bytes or a typed
//	*cascade.Error — never a panic, never a silent truncation.
//
// Constraints: golangci's plugins-providers-boundary depguard rule
//
//	(12-QUALITY-CONSTITUTION.md Art.10.2) forbids any non-test file under
//	internal/plugins/** from importing internal/** at all, so every
//	host-side dependency this package needs from internal/storage,
//	internal/secrets, or internal/hooks/egress is expressed as a small
//	local interface here (Deps' fields) — never a real import of those
//	packages. A composition-root package outside this ticket's
//	files_scope adapts the real internal/storage.PluginStorage,
//	internal/secrets.Broker, and internal/hooks/egress.Engine values into
//	these seams, matching internal/plugins/process/types.go's own
//	EgressRegistrar/EgressInterceptor pattern (established there as a
//	real golangci-lint failure, not a preference). The one exception is
//	pkg/plugin.Storage, which this package imports directly — it is a
//	pkg/ type, not internal/, so the boundary rule does not apply, and
//	internal/storage.PluginStorage already implements it (var _
//	plugin.Storage assertion in internal/storage/plugin.go).
//
//	The wire protocol below (envelope/result, fixed request/response
//	scratch offsets in the module's own linear memory) matches the
//	O/S-30.T6 spike's proven design exactly (ADR-N-S30-T6): all three
//	runtimes passed the same 21-subtest conformance suite against it. The
//	spike also found ONE real constraint this package must honor: fixed
//	offsets are unsafe for concurrent in-flight calls against the SAME
//	module instance. This package resolves it structurally, not by
//	locking: Dispatch creates a fresh module instance per call (task 1's
//	"one wazero store per dispatch for guest isolation"), so no two
//	in-flight calls ever share one instance's linear memory.
//
// SPORT: internal.plugins.wasm.Runtime/ADDED,
//
//	internal.plugins.wasm.HostABIVersion/ADDED (P1-E15-W4-S32-T1).
package wasm

import "encoding/json"

// HostABIVersionV1 is the host-ABI version this runtime implements. A
// guest module's exported "cascade_abi_version" i32 global must equal
// this value; Load refuses (hard typed error, never a silent
// degradation) any module whose global is present and does not match.
const HostABIVersionV1 = 1

// abiVersionGlobal is the exported wazero global name a guest module
// carries its host-ABI version in (the "custom WASM section" alternative
// the ticket names is a heavier binary-format dependency an exported
// global sidesteps; see loader.go's checkABIVersion).
const abiVersionGlobal = "cascade_abi_version"

// HostABIVersion reports the host-ABI version this runtime implements,
// for the S-32.T2 conformance suite to assert against.
func HostABIVersion() int { return HostABIVersionV1 }

// The seven ABI v1 host-function export names a guest module imports
// under wazero host-module name "env" (fixture.wasm's provenance
// documents the real WABT-compiled module using exactly these names).
const (
	hostFnHTTP         = "host_http"
	hostFnStorage      = "host_storage"
	hostFnLog          = "host_log"
	hostFnStream       = "host_stream"
	hostFnSecretRef    = "host_secretref"
	hostFnEventEmit    = "host_eventemit"
	hostFnToolRegister = "host_toolregister"
)

// wasmReqOffset and wasmRespOffset are the two linear-memory scratch
// regions every guest module and this runtime agree on: the guest writes
// a request payload at wasmReqOffset before calling a host import; the
// host function writes its response envelope at wasmRespOffset and
// returns the response's byte length. Safe only because Dispatch gives
// every call its own module instance (see package doc).
const (
	wasmReqOffset  = uint32(0)
	wasmRespOffset = uint32(65536)
)

// result is the wire shape every host function's response is marshaled
// as: exactly one of Payload or Error is set, never both.
type result struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Request/response payload shapes, one pair per host function. Every
// field the ABI needs is present; nothing beyond it, so a guest cannot
// smuggle extra semantics through an unvalidated field.

// HTTPRequest is host_http's request payload. URL is checked against the
// calling plugin's declared net scopes before Deps.Net is ever invoked.
type HTTPRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// HTTPResponse is host_http's response payload.
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// StorageRequest is host_storage's request payload; Op selects among
// get/put/delete/list.
type StorageRequest struct {
	Op    string `json:"op"`
	Key   string `json:"key,omitempty"`
	Value []byte `json:"value,omitempty"`
}

// StorageResponse is host_storage's response payload. Keys is set only
// for a list op; Value only for a get.
type StorageResponse struct {
	Value []byte   `json:"value,omitempty"`
	Keys  []string `json:"keys,omitempty"`
}

// LogRequest is host_log's request payload.
type LogRequest struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// LogResponse is host_log's response payload.
type LogResponse struct {
	Accepted bool `json:"accepted"`
}

// StreamRequest is host_stream's request payload.
type StreamRequest struct {
	ChannelID string `json:"channelId"`
	Data      []byte `json:"data"`
}

// StreamResponse is host_stream's response payload.
type StreamResponse struct {
	BytesWritten int `json:"bytesWritten"`
}

// SecretRefRequest is host_secretref's request payload.
type SecretRefRequest struct {
	Name string `json:"name"`
}

// SecretRefResponse is host_secretref's response payload. It has no
// field capable of carrying a literal secret value — RefID is the
// broker's opaque, short-lived reference token, resolved back to a
// value only by the broker itself at call time (§5.21).
type SecretRefResponse struct {
	RefID string `json:"refId"`
}

// EventEmitRequest is host_eventemit's request payload.
type EventEmitRequest struct {
	Topic   string `json:"topic"`
	Payload []byte `json:"payload"`
}

// EventEmitResponse is host_eventemit's response payload.
type EventEmitResponse struct {
	EventID string `json:"eventId"`
}

// ToolRegisterRequest is host_toolregister's request payload.
type ToolRegisterRequest struct {
	ToolName string `json:"toolName"`
	Schema   []byte `json:"schema"`
}

// ToolRegisterResponse is host_toolregister's response payload.
type ToolRegisterResponse struct {
	Registered bool `json:"registered"`
}
