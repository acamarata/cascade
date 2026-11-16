// Purpose: tests for the initialize handshake, including version
// negotiation when a client offers an unsupported protocol revision.

package mcp

import (
	"encoding/json"
	"testing"
)

// TestMCPNegotiatesSupportedVersion sends an initialize offering the
// unsupported "2026-07-28" revision and asserts the server answers with
// MCPProtocolVersion ("2025-11-25") rather than echoing or rejecting it.
func TestMCPNegotiatesSupportedVersion(t *testing.T) {
	params := initializeParams{
		ProtocolVersion: "2026-07-28",
		ClientInfo:      clientInfo{Name: "test-client", Version: "0.0.1"},
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	s := &Server{}
	frame := &Frame{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  MethodInitialize,
		Params:  paramsBytes,
	}

	resp := s.dispatchInitialize(frame)
	if resp == nil {
		t.Fatal("expected non-nil response from dispatchInitialize")
	}
	if resp.Error != nil {
		t.Fatalf("expected no error in response, got: %+v", resp.Error)
	}

	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	var result initializeResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProtocolVersion != MCPProtocolVersion {
		t.Errorf("protocol version = %q, want %q", result.ProtocolVersion, MCPProtocolVersion)
	}
}
