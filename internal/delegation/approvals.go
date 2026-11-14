// Purpose: bridge a delegated agent's tool-approval requests into the one
// policy approval queue. Every request the driver raises on
// ApprovalRequests becomes one ask-tier queue entry; the driver is answered
// with a token only after the queue redeemed it through ConsumeToken, and a
// denial, a cancel or a timeout answers it with an empty token and cancels
// the entry.
// Inputs: the queue, the spawn handle's driver, the delegation record and
// the approval timeout; the timeout runs on an injected runtime.Clock.
// Outputs: ResolveApproval calls on the driver and queue entries.
// Constraints: the bridge never decides anything itself, so there is no
// path that approves without a queue decision. EnqueueRequest has no Ref or
// Tier field, so the record id and the request's data class ride in Params,
// which the queue hashes at admission and re-checks at redemption.

package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CapabilityToolApproval is the capability every bridged request is queued
// under. RegisterToolApprovalCapability registers it once.
const CapabilityToolApproval = "agents.tool_approval"

const (
	// approvalAction is the action text every bridged entry is hashed under.
	approvalAction = "agent.approval"
	// approvalSummaryMax bounds the displayed summary. The contract allows
	// 512 bytes, but the queue's admission refuses display text that its
	// sanitizer would shorten (anything over 64 bytes), so the bridge cuts
	// to the stricter bound rather than having every long prompt refused.
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
// exactly once; when ctx ends every unanswered request is refused.
func BridgeApprovals(ctx context.Context, q policy.ApprovalQueue, h agents.SpawnHandle, rec jobs.DelegationRecord, timeout time.Duration) error {
	b := approvalBridge{q: q, h: h, rec: rec, timeout: timeout, clock: runtime.NewSystemClock(), poll: approvalPoll}
	return b.run(ctx)
}

// approvalBridge is one BridgeApprovals call with its time sources.
type approvalBridge struct {
	q       policy.ApprovalQueue
	h       agents.SpawnHandle
	rec     jobs.DelegationRecord
	timeout time.Duration
	clock   runtime.Clock
	poll    time.Duration
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
	params, err := json.Marshal(approvalParams{
		Ref: b.rec.ID, Job: string(req.JobID), Request: req.ID, Kind: req.Kind, Tier: req.DataClass.String(),
	})
	if err != nil {
		return errors.Join(err, b.h.Driver.ResolveApproval(req.ID, ""))
	}
	res, err := b.q.Enqueue(ctx, policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: b.rec.AgentPrincipal},
		Capability: CapabilityToolApproval,
		Level:      policy.L2,
		Action:     approvalAction,
		Params:     params,
		Summary:    approvalSummary(req.Prompt),
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
	if b.await(ctx, res.RequestID) {
		if _, err := b.q.ConsumeToken(ctx, policy.ConsumeRequest{
			RequestID: res.RequestID, Nonce: res.Token.Nonce, Action: approvalAction, Params: params,
		}); err == nil {
			return b.h.Driver.ResolveApproval(req.ID, res.Token.Nonce)
		}
	}
	return b.refuse(ctx, req.ID, res.RequestID)
}

// await reports true once the entry left the pending set before the
// timeout. Leaving the set means decided, expired or cancelled; only a
// successful ConsumeToken afterwards tells an approval from the rest.
func (b approvalBridge) await(ctx context.Context, id string) bool {
	deadline := b.clock.Now().Add(b.timeout)
	tick := time.NewTicker(b.poll)
	defer tick.Stop()
	for {
		pending, err := b.q.GetPending(ctx)
		if err != nil {
			return false
		}
		if !pendingHas(pending, id) {
			return true
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

// refuse answers the driver with an empty token and cancels the entry. A
// Cancel the queue refuses because the entry already closed (denied,
// expired) leaves nothing to withdraw, so its error is not reported.
func (b approvalBridge) refuse(ctx context.Context, driverID, queueID string) error {
	err := b.h.Driver.ResolveApproval(driverID, "")
	_ = b.q.Cancel(context.WithoutCancel(ctx), queueID)
	return err
}

func pendingHas(pending []policy.PendingEntry, id string) bool {
	for _, p := range pending {
		if p.RequestID == id {
			return true
		}
	}
	return false
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
