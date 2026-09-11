package plugins

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// This file is a risk spike (P1-E14-W3-S30-T6, 12-QUALITY-CONSTITUTION.md
// Art.12) prototyping the ABI v1 host-function set (O/S-32.T1's design) as
// a TEST-SCOPED interface, per R-21.276: the spike ships NO production
// code. internal/plugins/hostfn.go stays unwritten -- O/S-32.T1 (W4)
// remains its sole owner. Every type, adapter and test in this spike lives
// in a _test.go file or under testdata/.

// HostFn is the seven-function ABI v1 host surface (matching the set
// O/S-32.T1 defines and O/S-32.T2's conformance suite covers): HostHTTP,
// HostStorage, HostLog, HostStream, HostSecretRef, HostEventEmit,
// HostToolRegister. Every method takes a typed request and returns a
// typed response plus a structured error from pkg/cascade's frozen
// taxonomy (or context.Canceled, unwrapped, when ctx is done).
type HostFn interface {
	HostHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error)
	HostStorage(ctx context.Context, req *StorageRequest) (*StorageResponse, error)
	HostLog(ctx context.Context, req *LogRequest) (*LogResponse, error)
	HostStream(ctx context.Context, req *StreamRequest) (*StreamResponse, error)
	HostSecretRef(ctx context.Context, req *SecretRefRequest) (*SecretRefResponse, error)
	HostEventEmit(ctx context.Context, req *EventEmitRequest) (*EventEmitResponse, error)
	HostToolRegister(ctx context.Context, req *ToolRegisterRequest) (*ToolRegisterResponse, error)
}

// Typed request/response pairs, one per host function. Fields are
// intentionally minimal: this spike prototypes ABI shape and call
// dispatch, not the production wire format (that is O/S-32.T1's job).

type HTTPRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}
type HTTPResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type StorageRequest struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}
type StorageResponse struct {
	Value string `json:"value"`
}

type LogRequest struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
type LogResponse struct {
	Accepted bool `json:"accepted"`
}

type StreamRequest struct {
	ChannelID string `json:"channelId"`
	Data      string `json:"data"`
}
type StreamResponse struct {
	BytesWritten int `json:"bytesWritten"`
}

type SecretRefRequest struct {
	Name string `json:"name"`
}
type SecretRefResponse struct {
	RefID string `json:"refId"`
}

type EventEmitRequest struct {
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}
type EventEmitResponse struct {
	EventID string `json:"eventId"`
}

type ToolRegisterRequest struct {
	ToolName string `json:"toolName"`
	Schema   string `json:"schema"`
}
type ToolRegisterResponse struct {
	Registered bool `json:"registered"`
}

// maxPayloadBytes is the spike's size-limit boundary for the
// oversized-payload error-path subtest.
const maxPayloadBytes = 4096

// ErrNilHostFnRequest and ErrPayloadTooLarge are the two structured
// error-path sentinels every adapter returns, both wrapping frozen
// pkg/cascade Kinds rather than inventing new ones. A cancelled context
// returns ctx.Err() (context.Canceled) directly and unwrapped, per the
// ticket's error-path spec.
var (
	ErrNilHostFnRequest = cascade.New(cascade.KindInvalidInput, "host-fn request is nil")
	ErrPayloadTooLarge  = cascade.New(cascade.KindInvalidInput, "host-fn payload exceeds size limit")
)

// checkCommon runs the three shared preconditions every adapter method
// applies before doing any real work: context cancellation, nil request,
// and a payload-size probe supplied by the caller (each request type has
// a different "payload" field, so callers pass its length).
func checkCommon(ctx context.Context, isNil bool, payloadLen int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isNil {
		return ErrNilHostFnRequest
	}
	if payloadLen > maxPayloadBytes {
		return ErrPayloadTooLarge
	}
	return nil
}

// envelope is the wire shape every adapter (process JSON pipe, wazero
// linear-memory call) marshals a request into: a method tag plus the raw
// typed payload. The builtin adapter never serializes -- it calls Go
// methods directly -- but shares this shape so cross-adapter fixture
// responses are directly comparable.
type envelope struct {
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// result is the wire shape every non-builtin adapter's response is
// marshaled as: either a payload or a structured error, never both.
type result struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// The seven ABI v1 method tags, shared by every adapter's dispatch table
// so the 21-subtest matrix (7 fns x 3 runtimes) exercises the same name
// set everywhere.
const (
	methodHTTP         = "HostHTTP"
	methodStorage      = "HostStorage"
	methodLog          = "HostLog"
	methodStream       = "HostStream"
	methodSecretRef    = "HostSecretRef"
	methodEventEmit    = "HostEventEmit"
	methodToolRegister = "HostToolRegister"
)
