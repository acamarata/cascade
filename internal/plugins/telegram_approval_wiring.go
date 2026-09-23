package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// Purpose (this file): the real, daemon-RPC-backed cascadepa.ApprovalService
//   (P1-E23-W5-S48-T4) — the bridge's §5.24 approval.show / approval.grant /
//   approval.deny client, following cascadepa_wiring.go's rpcDoer /
//   lazy-path-resolution precedent (P1-EXEC-20260918's resolved_decisions)
//   rather than a new pattern.
//
// Inputs: a request id. Nothing else: the signed token, the nonce and the
//   action/params digest are internal/policy's own §5.24 secrets and this
//   file, like plugins/cascade-pa/telegram, never touches them.
//
// Outputs: cascadepa.PendingApproval (ShowPending) with Bridgeable computed
//   by calling the REAL policy.RemoteApprovabilityMatrix{}.CanBridge over
//   the entry's own ActionClass — never a re-derived classifier (R-16.60c);
//   a redemption or refusal (Grant/Deny), unwrapped, from the real
//   approval.grant/approval.deny RPC handlers.
//
// Constraints — HONEST GAP, load-bearing: Grant calls approval.grant with
//   ONLY {RequestID}. This is not an oversight; it is what "the signed
//   token bytes are loaded SERVER-SIDE... never from the callback payload"
//   (R-21.230) reduces to once the seam that would load them is inspected.
//   Grep confirms internal/policy.ApprovalSigner has ZERO production
//   callers anywhere in this tree, and cmd/cascade/daemon_unix_policy.go's
//   own composition comment says so explicitly: "Verifier and Attestor are
//   deliberately absent. No production approval-key source and no
//   production attestation helper exist in this tree, and both absences
//   are fail-closed... Attaching a placeholder to either would make an
//   unauthorized redemption report success." approval.grant is ALSO an
//   elevation-class verb (internal/rpc's elevationTable, "always: true"):
//   by the SAME design, it needs a fresh local attestation "IN ADDITION to
//   whatever token its handler will go on to verify" (internal/policy/verbs.go),
//   which a remote Telegram tap cannot supply and this file does not
//   fabricate. Until a future ticket wires a real ApprovalKeySource and a
//   real Attestor, Grant refuses every call with the daemon's own real,
//   honest "no approval verifier is enrolled" / "no attestation source is
//   enrolled" refusal — never a stand-in that reports success. Deny is NOT
//   elevation-class (absent from elevationTable) and redeems for real today.
//
// SPORT: internal/plugins:telegram-approval-wiring (ADD) — P1-E23-W5-S48-T4.

// telegramApprovalClientTimeout bounds every approval.* round trip, matching
// cascadePAClientTimeout's identical precedent for the same unix-socket
// transport.
const telegramApprovalClientTimeout = 5 * time.Second

// telegramApprovalService adapts internal/client's unix-socket JSON-RPC
// transport to cascadepa.ApprovalService. Path resolution is deferred to
// each call, never performed at construction, mirroring cascadePAClient.
type telegramApprovalService struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real rpcClient() construction — see
	// rpcDoer's own doc comment (cascadepa_wiring.go). Always nil in
	// production; only a same-package test ever sets it.
	doer rpcDoer
}

// newTelegramApprovalService builds a telegramApprovalService from its
// collaborators, injected so tests substitute a fake dialer and a fake
// path resolver without a real socket or a real home directory.
func newTelegramApprovalService(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *telegramApprovalService {
	return &telegramApprovalService{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

// rpcClient resolves the daemon socket path and builds the real transport,
// or returns the test-only doer when one was set. See cascadePAClient's
// identical method for the reasoning.
func (s *telegramApprovalService) rpcClient() (rpcDoer, error) {
	if s.doer != nil {
		return s.doer, nil
	}
	paths, err := s.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa/telegram: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), s.dial, s.timeout), nil
}

// ShowPending implements cascadepa.ApprovalService over the real
// approval.show RPC, then asks the REAL RemoteApprovabilityMatrix — not a
// re-derived rule — whether the resolved class may cross a bridge.
func (s *telegramApprovalService) ShowPending(ctx context.Context, requestID string) (cascadepa.PendingApproval, error) {
	rpc, err := s.rpcClient()
	if err != nil {
		return cascadepa.PendingApproval{}, err
	}
	var entry policy.PendingEntry
	if err := rpc.Do(ctx, client.ApprovalShowMethod, policy.ApprovalShowParams{RequestID: requestID}, &entry); err != nil {
		return cascadepa.PendingApproval{}, err
	}
	return cascadepa.PendingApproval{
		RequestID:  entry.RequestID,
		Summary:    entry.Summary,
		ExpiresAt:  entry.ExpiresAt,
		Bridgeable: (policy.RemoteApprovabilityMatrix{}).CanBridge(entry.ActionClass),
	}, nil
}

// Grant implements cascadepa.ApprovalService over the real approval.grant
// RPC, submitting ONLY the request id — see this file's header for why
// that is honest today, not incomplete.
func (s *telegramApprovalService) Grant(ctx context.Context, requestID string) error {
	rpc, err := s.rpcClient()
	if err != nil {
		return err
	}
	var res policy.ApprovalGrantResult
	return rpc.Do(ctx, client.ApprovalGrantMethod, policy.ApprovalGrantParams{RequestID: requestID}, &res)
}

// Deny implements cascadepa.ApprovalService over the real approval.deny
// RPC. Unlike Grant, this verb is not elevation-class and redeems for real.
func (s *telegramApprovalService) Deny(ctx context.Context, requestID string) error {
	rpc, err := s.rpcClient()
	if err != nil {
		return err
	}
	var res policy.DecisionResult
	return rpc.Do(ctx, client.ApprovalDenyMethod, policy.ApprovalDecisionParams{RequestID: requestID}, &res)
}

// compile-time proof the adapter really satisfies the plugin's own seam.
var _ cascadepa.ApprovalService = (*telegramApprovalService)(nil)
