// Package gateway provides spawn-gate decision types and contracts.
package gateway

import (
	"context"
	"time"
)

// NativeReason represents a justification for a spawn-gate decision.
type NativeReason string

const (
	// ReasonBrowserMCP indicates the request requires browser or MCP capabilities.
	ReasonBrowserMCP NativeReason = "browser/MCP"
	// ReasonPrivateData indicates the request involves private data that must remain local.
	ReasonPrivateData NativeReason = "private data"
	// ReasonGatewayDownBurnCapped indicates the gateway is down and burn limits are capped.
	ReasonGatewayDownBurnCapped NativeReason = "gateway down+burn capped"
	// ReasonNativeSignOff indicates the request requires native sign-off.
	ReasonNativeSignOff NativeReason = "native sign-off"
)

// Decision represents the outcome of a spawn-gate evaluation.
type Decision struct {
	// Allowed indicates whether the spawn request is permitted.
	Allowed bool
	// Reason is the primary justification for the decision.
	Reason NativeReason
	// RoutedTo specifies the execution target (e.g., "local", "cloud").
	RoutedTo string
	// RecordedAt is the timestamp when the decision was made.
	RecordedAt time.Time
}

// SpawnRequest encapsulates the parameters of a request to spawn a process or agent.
type SpawnRequest struct {
	// Prompt is the user's input or instruction.
	Prompt string
	// IsHeadless indicates whether the execution environment lacks a user interface.
	IsHeadless bool
	// Reasons is the list of justifications for requiring native execution.
	Reasons []NativeReason
	// GatewayStatus represents the current state of the gateway (e.g., "up", "down").
	GatewayStatus string
	// BurnAccounts is the list of accounts authorized to absorb execution costs.
	BurnAccounts []string
}

// Gate defines the contract for evaluating spawn requests.
type Gate interface {
	// Evaluate inspects a SpawnRequest and returns a Decision indicating whether
	// the request is allowed, why, and where it should be routed.
	Evaluate(ctx context.Context, req SpawnRequest) (Decision, error)
}
