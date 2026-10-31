package process

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HostRequest is a plugin request carrying its original correlation ID.
type HostRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// HostHTTPRequest describes an outbound request served by the host.
type HostHTTPRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// HostHTTPResponse is the bounded response returned to a plugin.
type HostHTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// HostServer executes calls after capability authorization.
type HostServer interface {
	ServeHTTP(context.Context, HostHTTPRequest) (HostHTTPResponse, error)
	ServeStorage(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ServeSecretRef(context.Context, string) (string, error)
	ServeLog(context.Context, string, string) error
	ServeEvent(context.Context, string, json.RawMessage) error
}

// HostRequests returns plugin requests separately from host call responses.
func (t *Transport) HostRequests() <-chan HostRequest { return t.hostRequests }

// Reply writes a correlated response with the taxonomy's stable error code.
func (t *Transport) Reply(id json.RawMessage, result any, err error) error {
	frame := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		frame["error"] = cascade.NewPluginRPCError(err)
	} else {
		frame["result"] = result
	}
	return t.writeFrame(frame)
}

func responseID(raw json.RawMessage) (uint64, bool) {
	var id uint64
	err := json.Unmarshal(raw, &id)
	return id, err == nil
}
