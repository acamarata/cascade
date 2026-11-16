// Purpose: bridge a delegated agent's tool-approval requests into the one
// policy approval queue: one ask-tier entry per request; the driver gets a
// token only after the witnessed approval.grant recorded the approval and
// ConsumeToken redeemed it. A denial, cancel, timeout, unattested decision
// or permissions grant past the envelope's Roots or Egress (never queued)
// answers an empty token.
// Inputs: queue, spawn handle, delegation record, timeout (injected clock).
// Outputs: ResolveApproval calls, queue entries, a journal row per refusal.
// Constraints: the bridge never decides anything itself. The record id and
// the request's data class ride in Params, which the queue hashes at
// admission and re-checks at redemption.

package delegation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// CapabilityToolApproval is the capability every bridged request is queued
// under. RegisterToolApprovalCapability registers it once.
const CapabilityToolApproval = "agents.tool_approval"

const (
	// approvalAction is the action text every bridged entry is hashed under.
	approvalAction = "agent.approval"
	// approvalSummaryMax bounds the displayed summary: the contract allows
	// 512 bytes, but queue admission refuses display text over 64 bytes.
	approvalSummaryMax = 64
	// approvalSummaryEmpty stands in for a request that carries no prompt.
	approvalSummaryEmpty = "agent approval request"
	// approvalPoll is how often the bridge looks for a decision.
	approvalPoll = 250 * time.Millisecond
)

// RegisterToolApprovalCapability adds CapabilityToolApproval to reg. A
// second call is refused by the registry as a conflict, which is what keeps
// the registration to exactly one.
func RegisterToolApprovalCapability(ctx context.Context, reg policy.CapabilityRegistry) error {
	return reg.Add(ctx, policy.Capability{
		Name:          CapabilityToolApproval,
		Desc:          "a delegated agent asks to run one tool action",
		DefaultPolicy: policy.ClassWorkspaceMutation,
	})
}

// BridgeApprovals serves every approval request h's driver raises until the
// channel closes or ctx ends. Requests are served concurrently, so a slow
// decision never holds back the next request. Each request is answered
// exactly once; when ctx ends every unanswered request is refused. A q
// that does not also implement grantWitness forwards nothing, ever.
func BridgeApprovals(ctx context.Context, q policy.ApprovalQueue, h agents.SpawnHandle, rec jobs.DelegationRecord, timeout time.Duration) error {
	b := approvalBridge{q: q, h: h, rec: rec, timeout: timeout, clock: runtime.NewSystemClock(), poll: approvalPoll, log: slog.Default()}
	return b.run(ctx)
}

// grantWitness is the audit effect log the witnessed approval.grant claims
// "approval.grant:<id>" in before it records an approval; *audit.EffectLog
// implements it. An owner-cli or other direct Decide leaves no claim.
type grantWitness interface {
	EffectState(ctx context.Context, key string) (audit.EffectPhase, bool, error)
}

// approvalBridge is one BridgeApprovals call with its time sources.
type approvalBridge struct {
	q       policy.ApprovalQueue
	h       agents.SpawnHandle
	rec     jobs.DelegationRecord
	timeout time.Duration
	clock   runtime.Clock
	poll    time.Duration
	log     *slog.Logger // nil journals to slog.Default
}

// approvalParams is what the queue hashes for one bridged request.
type approvalParams struct {
	Ref     string `json:"ref"`
	Job     string `json:"job"`
	Request string `json:"request"`
	Kind    string `json:"kind"`
	Tier    string `json:"tier"`
}

func (b approvalBridge) run(ctx context.Context) error {
	if b.q == nil || b.h.Driver == nil || b.clock == nil || b.timeout <= 0 || b.poll <= 0 {
		return cascade.New(cascade.KindInvalidInput,
			"delegation: the approval bridge needs a queue, a driver, a clock and a positive timeout")
	}
	ch := b.h.Driver.ApprovalRequests()
	if ch == nil {
		return nil
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	serve := func(req provider.ApprovalRequest) {
		if err := b.serve(ctx, req); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return errors.Join(errs...)
		case req, ok := <-ch:
			if !ok {
				wg.Wait()
				return errors.Join(errs...)
			}
			wg.Go(func() { serve(req) })
		}
	}
}

// serve answers one request: enqueue, wait for the decision, then forward a
// redeemed token or refuse.
func (b approvalBridge) serve(ctx context.Context, req provider.ApprovalRequest) error {
	if req.Kind == provider.ApprovalKindPermissions && !b.grantInEnvelope(req.Grant) {
		return errors.Join(b.journal(ctx, req.ID, cascade.Newf(cascade.KindPermissionDenied,
			"delegation: approval request %q asks for access outside the envelope; nothing queued", req.ID)),
			b.h.Driver.ResolveApproval(req.ID, ""))
	}
	params, err := json.Marshal(approvalParams{
		Ref: b.rec.ID, Job: string(req.JobID), Request: req.ID, Kind: req.Kind, Tier: req.DataClass.String(),
	})
	if err != nil {
		return errors.Join(err, b.h.Driver.ResolveApproval(req.ID, ""))
	}
	res, err := b.q.Enqueue(ctx, policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: b.rec.AgentPrincipal},
		Capability: CapabilityToolApproval, Level: policy.L2, Action: approvalAction, Params: params,
		Summary: approvalSummary(req.Prompt),
	})
	if err != nil {
		return errors.Join(err, b.h.Driver.ResolveApproval(req.ID, ""))
	}
	if res.Deduplicated {
		// Another request already owns this entry; answering this one with
		// that entry's token would approve it on someone else's decision.
		return errors.Join(cascade.Newf(cascade.KindConflict,
			"delegation: approval request %q repeats one already queued", req.ID),
			b.h.Driver.ResolveApproval(req.ID, ""))
	}
	deadline := b.clock.Now().Add(b.timeout)
	if !b.await(ctx, res.RequestID, deadline) {
		return b.refuse(ctx, req.ID, res.RequestID)
	}
	if !b.witnessed(ctx, res.RequestID, deadline) {
		// Also every denial, cancel or expiry, so the row is not an error.
		_ = b.journal(ctx, req.ID, cascade.Newf(cascade.KindPermissionDenied,
			"delegation: approval request %q has no confirmed witnessed approval.grant; nothing forwarded", req.ID))
		return b.refuse(ctx, req.ID, res.RequestID)
	}
	if _, err := b.q.ConsumeToken(ctx, policy.ConsumeRequest{
		RequestID: res.RequestID, Nonce: res.Token.Nonce, Action: approvalAction, Params: params,
	}); err == nil {
		return b.h.Driver.ResolveApproval(req.ID, res.Token.Nonce)
	}
	return b.refuse(ctx, req.ID, res.RequestID)
}

// await reports true once the entry left the pending set before the
// deadline. Leaving the set means decided, expired or cancelled; only a
// successful ConsumeToken afterwards tells an approval from the rest.
func (b approvalBridge) await(ctx context.Context, id string, deadline time.Time) bool {
	return b.until(ctx, deadline, func() (bool, bool) {
		pending, err := b.q.GetPending(ctx)
		return err != nil || !pendingHas(pending, id), err == nil
	})
}

// witnessed reports whether approval.grant's claim on id is confirmed. An
// open intent (begun before the grant's Decide) is polled until deadline.
func (b approvalBridge) witnessed(ctx context.Context, id string, deadline time.Time) bool {
	w, ok := b.q.(grantWitness)
	if !ok {
		return false
	}
	return b.until(ctx, deadline, func() (bool, bool) {
		phase, found, err := w.EffectState(ctx, policy.GrantEffectKey(id))
		settled := err != nil || !found || phase != audit.EffectIntent
		return settled, err == nil && found && phase == audit.EffectConfirmed
	})
}

// until runs check every poll until it reports done, then answers its ok.
// The deadline passing or ctx ending first answers false.
func (b approvalBridge) until(ctx context.Context, deadline time.Time, check func() (done, ok bool)) bool {
	tick := time.NewTicker(b.poll)
	defer tick.Stop()
	for {
		if done, ok := check(); done {
			return ok
		}
		if !b.clock.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// grantInEnvelope reports whether every path of g lies under an envelope
// root and every host is one of its egress destinations. A missing grant,
// an envelope that does not decode or a relative path is outside.
func (b approvalBridge) grantInEnvelope(g *provider.PermissionGrant) bool {
	signed, err := envelope.Decode(b.rec.Signed)
	if g == nil || err != nil {
		return false
	}
	for _, raw := range g.Paths {
		p := path.Clean(raw)
		under := func(root string) bool {
			root = path.Clean(root)
			return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
		}
		if !path.IsAbs(raw) || !slices.ContainsFunc(signed.Env.Roots, under) {
			return false
		}
	}
	for _, h := range g.Hosts {
		if !slices.Contains(signed.Env.Egress, h) {
			return false
		}
	}
	return true
}

// journal writes one refusal row naming the record and request, then
// returns err so the caller reports it too.
func (b approvalBridge) journal(ctx context.Context, requestID string, err error) error {
	cmp.Or(b.log, slog.Default()).WarnContext(ctx, "delegation: approval refused", "ref", b.rec.ID, "request", requestID, "reason", err.Error())
	return err
}

// refuse answers the driver with an empty token and cancels the entry. A
// Cancel the queue refuses because the entry already closed (denied,
// expired) leaves nothing to withdraw, so its error is not reported.
func (b approvalBridge) refuse(ctx context.Context, driverID, queueID string) error {
	err := b.h.Driver.ResolveApproval(driverID, "")
	_ = b.q.Cancel(context.WithoutCancel(ctx), queueID)
	return err
}

func pendingHas(pending []policy.PendingEntry, id string) bool {
	return slices.ContainsFunc(pending, func(p policy.PendingEntry) bool { return p.RequestID == id })
}

// approvalSummary is the prompt with control characters blanked, trimmed
// and cut at a rune boundary to approvalSummaryMax bytes.
func approvalSummary(prompt string) string {
	s := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, prompt)
	s = strings.TrimSpace(s)
	if s == "" {
		return approvalSummaryEmpty
	}
	if len(s) > approvalSummaryMax {
		s = s[:approvalSummaryMax]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
