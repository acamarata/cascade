//go:build !windows

package process

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HostCapabilityChecker authorizes plugin operations before host execution.
type HostCapabilityChecker interface {
	CheckHTTP(context.Context, string) error
	CheckStorage(context.Context, string) error
	CheckSecretRef(context.Context, string) (any, error)
	CheckGeneric(context.Context, string) error
}

// consumeHostCalls drains one transport through EOF, including after restart.
func (rt *ProcessRuntime) consumeHostCalls(ctx context.Context, t *Transport) {
	requests, notifications := t.HostRequests(), t.Notifications()
	for requests != nil || notifications != nil {
		select {
		case r, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			result, err := rt.serveHostCall(ctx, r)
			_ = t.Reply(r.ID, result, err)
		case n, ok := <-notifications:
			if !ok {
				notifications = nil
				continue
			}
			rt.checkHostCall(ctx, n)
		}
	}
}

// checkHostCall checks authority but never executes notification operations.
func (rt *ProcessRuntime) checkHostCall(ctx context.Context, n Notification) {
	if !strings.HasPrefix(n.Method, "host_") {
		return
	}
	_ = rt.authorizeHostCall(ctx, n.Method, n.Params)
	var params map[string]json.RawMessage
	_ = json.Unmarshal(n.Params, &params)
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if sink, ok := rt.CapabilityChecker.(interface {
		AuditHostNotification(context.Context, string, []string)
	}); ok {
		sink.AuditHostNotification(ctx, n.Method, keys)
	}
}

func (rt *ProcessRuntime) authorizeHostCall(ctx context.Context, method string, params json.RawMessage) error {
	c := rt.CapabilityChecker
	if c == nil {
		return cascade.New(cascade.KindUnavailable, "host boundary not wired")
	}
	switch {
	case method == "host_http_request":
		return c.CheckHTTP(ctx, decodeHostParam(params, "url"))
	case method == "host_secret_ref":
		_, err := c.CheckSecretRef(ctx, decodeHostParam(params, "key"))
		return err
	case strings.HasPrefix(method, "host_storage_"):
		return c.CheckStorage(ctx, decodeHostParam(params, "domain"))
	case method == "host_event_emit", method == "host_stream_emit", method == "host_tool_register":
		return c.CheckGeneric(ctx, strings.Replace(method, "host_", "host.", 1))
	case method == "host_log":
		return nil
	default:
		return cascade.New(cascade.KindUnsupported, "unsupported host method")
	}
}

func (rt *ProcessRuntime) serveHostCall(ctx context.Context, r HostRequest) (any, error) {
	if err := rt.authorizeHostCall(ctx, r.Method, r.Params); err != nil {
		return nil, err
	}
	if r.Method == "host_tool_register" {
		// Authorized and audited above; the process boundary has no tool
		// registration service, so an allowed call is still not served.
		return nil, cascade.New(cascade.KindUnsupported, "host tool registration is not served over the process boundary")
	}
	s, ok := rt.CapabilityChecker.(HostServer)
	if !ok {
		return nil, cascade.New(cascade.KindUnavailable, "host boundary not wired")
	}
	switch {
	case r.Method == "host_http_request":
		var req HostHTTPRequest
		if json.Unmarshal(r.Params, &req) != nil {
			return nil, cascade.ErrInvalidInput
		}
		return s.ServeHTTP(ctx, req)
	case strings.HasPrefix(r.Method, "host_storage_"):
		return s.ServeStorage(ctx, strings.TrimPrefix(r.Method, "host_storage_"), r.Params)
	case r.Method == "host_secret_ref":
		return s.ServeSecretRef(ctx, decodeHostParam(r.Params, "key"))
	case r.Method == "host_log":
		return nil, s.ServeLog(ctx, decodeHostParam(r.Params, "level"), decodeHostParam(r.Params, "msg"))
	default:
		var p struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(r.Params, &p) != nil {
			return nil, cascade.ErrInvalidInput
		}
		return nil, s.ServeEvent(ctx, p.Kind, p.Payload)
	}
}

func decodeHostParam(raw json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(m[field], &value)
	return value
}
