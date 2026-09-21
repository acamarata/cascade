package daemon

// Purpose: the daemon's home for Epic W's personal-assistant bridge — the
//   subsystem that owns the Telegram long-poll goroutine for the daemon's
//   whole life, the drain that stops it at shutdown, and the pa.pair_code
//   RPC that makes `cascade pa pair` issue a code IN THE SAME PROCESS that
//   verifies it.
//
// WHY THIS FILE EXISTS, in one sentence: without it the poll loop's context
//   was the `cascade pa pair` CLI command's, so it died at process exit —
//   before the first 30-second getUpdates even returned — and an operator
//   who typed "/pair <code>" into Telegram was talking to nothing.
//
// Inputs: a BridgeSubsystem of BARE FUNCS the composition root composed, the
//   subsystems' context, and the RPC registry.
// Outputs: the poll goroutine started under this Manifest (so Wait joins it),
//   its drain wired to that context ending, and pa.pair_code bound.
//
// Constraints, each one deliberate:
//   - BARE FUNCS, NOT A PLUGIN TYPE. internal/daemon must not import
//     internal/plugins: internal/plugins -> internal/client -> internal/daemon
//     is a real, compiler-proven cycle. RegisterHarnessSessionWatch already
//     takes a bare func for exactly this reason; this seam follows it.
//   - ISSUANCE IS NOT GATED ON THE POLL. A bridge whose module is disabled or
//     whose token does not resolve still registers pa.pair_code (the verb
//     answers with the composition root's own typed refusal), because a
//     command that vanishes when the thing it configures is off is
//     undiagnosable. What IS gated is the goroutine.
//   - THE DRAIN IS NOT OPTIONAL. Stop blocks until the poll goroutine has
//     actually returned, and it runs on ITS OWN timeout budget derived from a
//     context that is already cancelled (context.WithoutCancel), or the drain
//     would be cancelled by the very shutdown that triggered it.
//
// SPORT: internal/daemon:bridge-subsystem (ADD) — P1-E23-W5-S48-T1.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// bridgeSubsystem is the fail-loud Manifest name this subsystem reports under
// (R-14.87).
const bridgeSubsystem = "cascade-pa.bridge"

// MethodBridgePairCode is the JSON-RPC verb `cascade pa pair` dials. The
// daemon owns issuance because the daemon owns the verifier: a code minted in
// one process and checked in another is either a shared plaintext credential
// or a shared key, and neither is something this flow needs.
const MethodBridgePairCode = "pa.pair_code"

// bridgeDrainTimeout bounds the shutdown drain. A poll loop parked in a
// 30-second long poll returns as soon as its context is cancelled, so this is
// generous by an order of magnitude; it exists so a wedged transport cannot
// hold the daemon's shutdown open forever.
const bridgeDrainTimeout = 10 * time.Second

// BridgePairCodeParams is pa.pair_code's request body. An empty Subject asks
// the bridge for its own configured subject rather than a made-up default.
type BridgePairCodeParams struct {
	Subject string `json:"subject,omitempty"`
}

// BridgePairCodeResult is pa.pair_code's reply. ExpiresAt is RFC3339 so the
// wire carries an instant a human can read in a log, matching every other
// time this daemon puts on the socket.
type BridgePairCodeResult struct {
	Code      string `json:"code"`
	Subject   string `json:"subject"`
	ExpiresAt string `json:"expires_at"`
}

// BridgeSubsystem is the composition root's bare-func description of one
// assembled bridge.
//
// Start launches the poll and returns immediately; a nil Start means the
// bridge is not enabled on this host, which is a DISABLED subsystem, not a
// failed one. Stop cancels and DRAINS. IssueCode backs pa.pair_code; a nil
// IssueCode leaves the verb unregistered.
type BridgeSubsystem struct {
	Start     func(ctx context.Context) error
	Stop      func(ctx context.Context) error
	IssueCode func(ctx context.Context, subject string) (BridgePairCodeResult, error)
	// DisabledReason is what `cascade daemon status` shows when Start is nil
	// — the operator-facing answer to "why is my bot not responding".
	DisabledReason string
}

// RegisterBridgeModule mounts sub as a tracked daemon subsystem and binds
// pa.pair_code.
//
// It never fails the daemon's startup: a bridge that cannot run is a missing
// convenience, not a broken daemon, and the manifest records its state either
// way for `cascade daemon status` to report (the same posture
// RegisterHarnessSessionWatch takes).
func (m *Manifest) RegisterBridgeModule(ctx context.Context, registry *rpc.Registry, sub BridgeSubsystem) {
	m.Register(bridgeSubsystem)
	if registry != nil && sub.IssueCode != nil && !registry.Registered(MethodBridgePairCode) {
		registry.Register(MethodBridgePairCode, bridgePairCodeHandler(sub.IssueCode))
	}
	if sub.Start == nil {
		m.Disabled(bridgeSubsystem, disabledReasonOr(sub.DisabledReason))
		return
	}
	if err := sub.Start(ctx); err != nil {
		m.Failed(bridgeSubsystem, err.Error())
		return
	}
	m.goSubsystem(func() { m.drainBridge(ctx, sub.Stop) })
	m.Started(bridgeSubsystem, "polling for bridge updates")
}

// drainBridge waits for the subsystems' context to end and then stops the
// poll, blocking until it has really exited.
//
// This runs as a tracked goroutine so Manifest.Wait joins the DRAIN, not just
// the poll: a caller that cancels and then waits is otherwise told the
// subsystem is finished while an in-flight request is still using its
// resources — the same defect goSubsystem's own comment records for the
// worktree sweeper.
func (m *Manifest) drainBridge(ctx context.Context, stop func(context.Context) error) {
	<-ctx.Done()
	if stop == nil {
		return
	}
	// WithoutCancel, because ctx is already cancelled: a drain budget derived
	// from the cancelled context would expire before the first select.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bridgeDrainTimeout)
	defer cancel()
	if err := stop(stopCtx); err != nil {
		m.Failed(bridgeSubsystem, "drain: "+err.Error())
	}
}

// disabledReasonOr keeps the manifest detail non-empty: "disabled" with no
// reason is the silence R-14.87 exists to forbid.
func disabledReasonOr(reason string) string {
	if reason == "" {
		return "no bridge module is enabled"
	}
	return reason
}

// bridgePairCodeHandler adapts the composition root's issuance closure onto
// the registry. A nil params body is a valid request (issue for the bridge's
// own subject), so only MALFORMED params are refused.
func bridgePairCodeHandler(issue func(context.Context, string) (BridgePairCodeResult, error)) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params BridgePairCodeParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err,
					MethodBridgePairCode+": decode params")
			}
		}
		return issue(ctx, params.Subject)
	}
}
