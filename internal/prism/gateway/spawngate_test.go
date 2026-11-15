// Package gateway provides spawn-gate decision types and contracts.
package gateway

import (
	"context"
	"testing"
	"time"
)

// TestTypesCompile verifies that all SpawnGate types, constants, and the Gate
// interface compile correctly and expose the expected fields and methods.
func TestTypesCompile(t *testing.T) {
	// Verify NativeReason constants match the allowed set.
	_ = ReasonBrowserMCP
	_ = ReasonPrivateData
	_ = ReasonGatewayDownBurnCapped
	_ = ReasonNativeSignOff

	if ReasonBrowserMCP != "browser/MCP" {
		t.Errorf("unexpected ReasonBrowserMCP: %s", ReasonBrowserMCP)
	}
	if ReasonPrivateData != "private data" {
		t.Errorf("unexpected ReasonPrivateData: %s", ReasonPrivateData)
	}
	if ReasonGatewayDownBurnCapped != "gateway down+burn capped" {
		t.Errorf("unexpected ReasonGatewayDownBurnCapped: %s", ReasonGatewayDownBurnCapped)
	}
	if ReasonNativeSignOff != "native sign-off" {
		t.Errorf("unexpected ReasonNativeSignOff: %s", ReasonNativeSignOff)
	}

	// Verify Decision struct fields.
	d := Decision{
		Allowed:    true,
		Reason:     ReasonBrowserMCP,
		RoutedTo:   "local",
		RecordedAt: time.Now(),
	}
	_ = d.Allowed
	_ = d.Reason
	_ = d.RoutedTo
	_ = d.RecordedAt

	// Verify SpawnRequest struct fields.
	req := SpawnRequest{
		Prompt:        "test prompt",
		IsHeadless:    false,
		Reasons:       []NativeReason{ReasonPrivateData},
		GatewayStatus: "up",
		BurnAccounts:  []string{"acct1"},
	}
	_ = req.Prompt
	_ = req.IsHeadless
	_ = req.Reasons
	_ = req.GatewayStatus
	_ = req.BurnAccounts

	// Verify Gate interface exposes Evaluate with the correct signature.
	var g Gate = &compileGate{}
	_, _ = g.Evaluate(context.Background(), req)
}

// compileGate is a test-only implementation of Gate to verify the interface compiles.
type compileGate struct{}

// Evaluate satisfies the Gate interface for compilation testing.
func (c *compileGate) Evaluate(_ context.Context, _ SpawnRequest) (Decision, error) {
	return Decision{}, nil
}
