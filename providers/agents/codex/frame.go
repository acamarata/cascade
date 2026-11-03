package codex

// Purpose: decode and encode App Server stdio frames (one JSON object per
// line, the JSON-RPC shape codex uses: id/method/params, id/result,
// id/error, method/params).
// Inputs: one raw line from the child's stdout.
// Outputs: a classified frame, or ErrMalformedFrame (wrapped) for a
// non-JSON line or a JSON-RPC shape violation.
// Constraints: unknown fields inside a known shape are ignored; an
// unknown method is not an error here.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"bytes"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// frameKind classifies a decoded frame.
type frameKind uint8

const (
	frameRequest      frameKind = iota + 1 // server -> client request (id + method)
	frameNotification                      // method, no id
	frameResponse                          // id + result or error, no method
)

// frame is one decoded App Server message.
type frame struct {
	Kind   frameKind
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *rpcError
	Raw    []byte
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type wireFrame struct {
	ID     json.RawMessage `json:"id"`
	Method *string         `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// decodeFrame classifies one line. Anything that is not a JSON object of
// one of the three shapes fails typed.
func decodeFrame(line []byte) (frame, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return frame{}, malformed("not a JSON object")
	}
	var w wireFrame
	if err := json.Unmarshal(line, &w); err != nil {
		return frame{}, malformed("not JSON")
	}
	f := frame{Raw: append([]byte(nil), line...), Params: w.Params, Result: w.Result}
	hasID := len(w.ID) > 0 && !bytes.Equal(w.ID, []byte("null"))
	if hasID && !validID(w.ID) {
		return frame{}, malformed("id is neither a number nor a string")
	}
	f.ID = w.ID
	switch {
	case w.Method != nil:
		if *w.Method == "" || len(w.Result) > 0 || len(w.Error) > 0 {
			return frame{}, malformed("method frame carries a result or error")
		}
		f.Method, f.Kind = *w.Method, frameNotification
		if hasID {
			f.Kind = frameRequest
		}
	case hasID && (len(w.Result) > 0) != (len(w.Error) > 0):
		f.Kind = frameResponse
		if len(w.Error) > 0 {
			f.Error = &rpcError{}
			if err := json.Unmarshal(w.Error, f.Error); err != nil {
				return frame{}, malformed("error is not an error object")
			}
		}
	default:
		return frame{}, malformed("neither request, notification nor response")
	}
	return f, nil
}

func validID(id json.RawMessage) bool {
	var n json.Number
	if json.Unmarshal(id, &n) == nil {
		return true
	}
	var s string
	return json.Unmarshal(id, &s) == nil
}

func malformed(why string) error {
	return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedFrame, "codex: malformed frame: %s", why)
}

// encodeRequest renders a client request line.
func encodeRequest(id int64, method string, params any) ([]byte, error) {
	return encodeLine(map[string]any{"id": id, "method": method, "params": params})
}

// encodeNotification renders a client notification line.
func encodeNotification(method string) ([]byte, error) {
	return encodeLine(map[string]any{"method": method})
}

// encodeResult renders a response to a server request.
func encodeResult(id json.RawMessage, result any) ([]byte, error) {
	return encodeLine(map[string]any{"id": id, "result": result})
}

// encodeError renders an error response to a server request.
func encodeError(id json.RawMessage, code int, msg string) ([]byte, error) {
	return encodeLine(map[string]any{"id": id, "error": rpcError{Code: code, Message: msg}})
}

func encodeLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "codex: encoding a frame")
	}
	return append(b, '\n'), nil
}
