//go:build !windows

package process

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/acamarata/cascade/pkg/cascade"
	"io"
	"strings"
	"testing"
	"time"
)

type hostTestServer struct {
	calls  int
	denied bool
	keys   []string
	event  string
	caps   []string
}

func (s *hostTestServer) CheckHTTP(context.Context, string) error {
	if s.denied {
		return cascade.ErrCapabilityDenied
	}
	return nil
}
func (s *hostTestServer) CheckStorage(context.Context, string) error          { return nil }
func (s *hostTestServer) CheckSecretRef(context.Context, string) (any, error) { return nil, nil }
func (s *hostTestServer) CheckGeneric(_ context.Context, capability string) error {
	s.caps = append(s.caps, capability)
	return nil
}
func (s *hostTestServer) ServeHTTP(context.Context, HostHTTPRequest) (HostHTTPResponse, error) {
	s.calls++
	return HostHTTPResponse{Status: 201, Body: "served"}, nil
}
func (s *hostTestServer) ServeStorage(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	s.calls++
	return nil, nil
}
func (s *hostTestServer) ServeSecretRef(context.Context, string) (string, error) {
	s.calls++
	return "ref", nil
}
func (s *hostTestServer) ServeLog(context.Context, string, string) error { s.calls++; return nil }
func (s *hostTestServer) ServeEvent(_ context.Context, kind string, _ json.RawMessage) error {
	s.calls++
	s.event = kind
	return nil
}
func (s *hostTestServer) AuditHostNotification(_ context.Context, _ string, keys []string) {
	s.keys = keys
}

func hostRoundTrip(t *testing.T, checker HostCapabilityChecker, method string) (json.RawMessage, *RPCError) {
	t.Helper()
	input, writer := io.Pipe()
	var out bytes.Buffer
	tr := NewTransport(&out, input, time.Second)
	rt := &ProcessRuntime{CapabilityChecker: checker}
	done := make(chan struct{})
	go func() { rt.consumeHostCalls(context.Background(), tr); close(done) }()
	_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":"plugin-id","method":"`+method+`","params":{"url":"https://api.example.com","kind":"change","payload":{}}}`+"\n")
	_ = writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("no host reply")
	}
	var reply struct {
		ID     string
		Result json.RawMessage
		Error  *RPCError
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != "plugin-id" {
		t.Fatal("lost correlation")
	}
	return reply.Result, reply.Error
}

func TestHostCallNilCheckerRepliesUnavailable(t *testing.T) {
	_, err := hostRoundTrip(t, nil, "host_http_request")
	if err == nil || err.Code != cascade.RPCCodeUnavailable || !strings.Contains(err.Message, "host boundary not wired") {
		t.Fatalf("error = %+v", err)
	}
}

func TestHostCallCheckerWithoutServerRepliesUnavailable(t *testing.T) {
	_, err := hostRoundTrip(t, capabilitySpy{}, "host_http_request")
	if err == nil || err.Code != cascade.RPCCodeUnavailable {
		t.Fatalf("error = %+v", err)
	}
}
func TestHostCallDeniedRepliesCapabilityDenied(t *testing.T) {
	s := &hostTestServer{denied: true}
	_, err := hostRoundTrip(t, s, "host_http_request")
	if err == nil || err.Code != cascade.RPCCodeCapabilityDenied || s.calls != 0 {
		t.Fatalf("denial = %+v calls=%d", err, s.calls)
	}
}
func TestHostCallAllowedReceivesResult(t *testing.T) {
	s := &hostTestServer{}
	result, err := hostRoundTrip(t, s, "host_http_request")
	if err != nil || !strings.Contains(string(result), "served") || s.calls != 1 {
		t.Fatalf("result=%s error=%+v calls=%d", result, err, s.calls)
	}
}
func TestHostCallStreamEmitRoutesToServeEvent(t *testing.T) {
	s := &hostTestServer{}
	_, err := hostRoundTrip(t, s, "host_stream_emit")
	if err != nil || s.calls != 1 || s.event != "change" {
		t.Fatalf("event=%s error=%+v", s.event, err)
	}
}
func TestHostCallToolRegisterIsCheckedThenUnsupported(t *testing.T) {
	s := &hostTestServer{}
	_, err := hostRoundTrip(t, s, "host_tool_register")
	if err == nil || err.Code != cascade.RPCCodeUnsupported || s.calls != 0 || strings.Join(s.caps, " ") != "host.tool_register" {
		t.Fatalf("error=%+v calls=%d caps=%v", err, s.calls, s.caps)
	}
}
func TestHostCallUnknownMethodRepliesUnsupported(t *testing.T) {
	s := &hostTestServer{}
	_, err := hostRoundTrip(t, s, "host_unknown")
	if err == nil || err.Code != cascade.RPCCodeUnsupported || s.calls != 0 {
		t.Fatalf("error=%+v calls=%d", err, s.calls)
	}
}
func TestHostNotificationNeverExecuted(t *testing.T) {
	s := &hostTestServer{}
	rt := &ProcessRuntime{CapabilityChecker: s}
	rt.checkHostCall(context.Background(), Notification{Method: "host_secret_ref", Params: json.RawMessage(`{"value":"private","key":"name"}`)})
	if s.calls != 0 || strings.Join(s.keys, " ") != "key value" {
		t.Fatalf("calls=%d keys=%v", s.calls, s.keys)
	}
}
