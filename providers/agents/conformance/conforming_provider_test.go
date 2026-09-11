// Purpose: conformingProvider — an AgentProvider double that genuinely
//
//	implements the contract (entitlement gating read from its own declared
//	CompliancePosture, distinct process-group identity per job, an
//	observable Cancel state transition, a context-bounded Collect, and a
//	CollectResult.DataClass computed through the real
//	provider.JoinDataClass) rather than one merely permissive enough to
//	pass. Running Suite against it IN-PROCESS (inprocess_test.go) attributes
//	real statement coverage to suite.go/cases_lifecycle.go/cases_security.go
//	— something the existing child-process non-vacuity proof
//	(TestSuiteFailsAgainstEmptyImplementation in suite_test.go) structurally
//	cannot do, since a re-exec'd child's coverage counters are never merged
//	into the parent process's profile.
//
// Constraints: conformingProvider exists ONLY in this _test.go file
//
//	(Art.1.1); no driver ships from this ticket. Collect never copies
//	AgentJobSpec.Prompt into CollectResult.Output or Artifacts — a driver
//	that echoed its prompt back would leak whatever secret or canary the
//	caller spawned it with, which is exactly what
//	TestCredentialCanaryFailsClosed checks.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import (
	"context"
	"fmt"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

// conformingJob is one spawned job's mutable state.
type conformingJob struct {
	spec      provider.AgentJobSpec
	state     provider.AgentRunState
	artifacts []string
}

// conformingProvider is a real, contract-honoring AgentProvider double.
type conformingProvider struct {
	mu       sync.Mutex
	jobs     map[provider.AgentJobID]*conformingJob
	next     int
	entitled bool
	// uncooperativeCancel, when true, makes Cancel report the honest
	// ErrCancelUnconfirmed (the job moves to Cancelling and stays there)
	// instead of confirming exit, modeling a child that never confirms
	// within the cancel deadline.
	uncooperativeCancel bool
	approvals           chan provider.ApprovalRequest
}

func newConformingProvider() *conformingProvider {
	return &conformingProvider{
		jobs:      make(map[provider.AgentJobID]*conformingJob),
		entitled:  true,
		approvals: make(chan provider.ApprovalRequest),
	}
}

// newConformingProviderNotEntitled is otherwise identical to
// newConformingProvider but honestly declares no programmatic entitlement,
// so Spawn genuinely refuses with ErrEntitlement rather than the refusal
// branch being dead code no case ever reaches.
func newConformingProviderNotEntitled() *conformingProvider {
	p := newConformingProvider()
	p.entitled = false
	return p
}

// newConformingProviderUncooperativeCancel is otherwise identical to
// newConformingProvider but never confirms a cancelled job's exit.
func newConformingProviderUncooperativeCancel() *conformingProvider {
	p := newConformingProvider()
	p.uncooperativeCancel = true
	return p
}

// newConformingProviderWithPendingApproval is otherwise identical to
// newConformingProvider but starts with one genuine ApprovalRequest
// already queued on its own channel, so a caller reading
// ApprovalRequests() observes a real request rather than an empty window.
func newConformingProviderWithPendingApproval() *conformingProvider {
	p := newConformingProvider()
	p.approvals = make(chan provider.ApprovalRequest, 1)
	p.approvals <- provider.ApprovalRequest{
		ID: "approval-1", JobID: "conforming-job-pending",
		Prompt: "review before continuing", DataClass: provider.DataClassInternal,
	}
	return p
}

var _ provider.AgentProvider = newConformingProvider()

func (p *conformingProvider) Chat(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: "ok"},
		FinishReason: "stop",
	}, nil
}

func (p *conformingProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{Vectors: make([][]float32, len(req.Inputs))}, nil
}

func (p *conformingProvider) Count(_ context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{Tokens: len(req.Text)}, nil
}

func (p *conformingProvider) Stream(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}

// Capabilities publishes a fixed CompliancePosture with
// ProgrammaticEntitlement set from p.entitled, so Spawn's gate below reads
// the same value a caller inspecting Capabilities would see.
func (p *conformingProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	p.mu.Lock()
	entitled := p.entitled
	p.mu.Unlock()
	return provider.Capabilities{
		CompliancePosture: provider.NewCompliancePosture(
			[]string{"api-key"}, false, entitled, []string{"agent"}, "steady", false,
		),
	}, nil
}

// Spawn refuses with ErrEntitlement when Capabilities reports no
// programmatic entitlement, exactly as AgentProvider.Spawn documents, and
// assigns each job its own process-group id so
// TestSpawnResultCarriesProcessGroup's distinctness assertion is exercised
// for real rather than vacuously satisfied by a constant in-process zero.
func (p *conformingProvider) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	caps, err := p.Capabilities(ctx, "")
	if err != nil {
		return provider.SpawnResult{}, err
	}
	if !caps.CompliancePosture.ProgrammaticEntitlement {
		return provider.SpawnResult{}, provider.ErrEntitlement
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	pgid := 2000 + p.next
	id := provider.AgentJobID(fmt.Sprintf("conforming-job-%d", p.next))
	artifacts := []string{fmt.Sprintf("artifact-%d.log", pgid)}
	p.jobs[id] = &conformingJob{spec: spec, state: provider.AgentRunRunning, artifacts: artifacts}
	return provider.SpawnResult{JobID: id, ProcessGroupID: pgid}, nil
}

// ApprovalRequests returns the driver's own approval channel: this double
// never resolves an approval it itself raised (it raises none for the
// suite's trivial jobs), so the channel simply carries nothing during the
// case's short observation window — the R-21.171 no-self-approval rule
// holds because there is nothing on the other end answering it.
func (p *conformingProvider) ApprovalRequests() <-chan provider.ApprovalRequest {
	return p.approvals
}

func (p *conformingProvider) ResolveApproval(string, string) error { return nil }

func (p *conformingProvider) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.jobs[id]; !ok {
		return provider.ErrJobNotFound
	}
	return nil
}

func (p *conformingProvider) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	job, ok := p.jobs[id]
	if !ok {
		return "", provider.ErrJobNotFound
	}
	return job.state, nil
}

// Cancel drives the R-21.174 ladder's OBSERVABLE contract: a live job
// moves through Cancelling into Cancelled before Cancel returns, so a
// caller polling Status immediately afterward never observes it silently
// still running — the exact defect TestCancelRunning and
// TestCancelUncooperativeChild assert against.
func (p *conformingProvider) Cancel(_ context.Context, id provider.AgentJobID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	job, ok := p.jobs[id]
	if !ok {
		return provider.ErrJobNotFound
	}
	if job.state.Terminal() {
		return nil
	}
	job.state = provider.AgentRunCancelling
	if p.uncooperativeCancel {
		return provider.ErrCancelUnconfirmed
	}
	job.state = provider.AgentRunCancelled
	return nil
}

// Collect completes job's work inline (this double models no real
// background process, so there is nothing to wait for beyond ctx) and
// returns a CollectResult whose Output is derived from the job's identity
// and prompt LENGTH only, never the prompt's content, and whose DataClass
// is the real provider.JoinDataClass of the spec's declared class with
// what execution observed — never a hardcoded most-restrictive constant.
func (p *conformingProvider) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	p.mu.Lock()
	job, ok := p.jobs[id]
	p.mu.Unlock()
	if !ok {
		return provider.CollectResult{}, provider.ErrJobNotFound
	}
	select {
	case <-ctx.Done():
		return provider.CollectResult{}, ctx.Err()
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !job.state.Terminal() {
		job.state = provider.AgentRunAccepted
	}
	observed := provider.DataClassPublic
	return provider.CollectResult{
		JobID:     id,
		Output:    fmt.Sprintf("job %s completed processing %d bytes of prompt", id, len(job.spec.Prompt)),
		Artifacts: job.artifacts,
		DataClass: provider.JoinDataClass(job.spec.DataClass, observed),
	}, nil
}

func (p *conformingProvider) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	job, ok := p.jobs[id]
	if !ok {
		return nil, provider.ErrJobNotFound
	}
	return job.artifacts, nil
}

func (p *conformingProvider) SupportedProtocols() provider.ProtocolRange {
	return provider.ProtocolRange{Min: "v1", Max: "v1"}
}

func (p *conformingProvider) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(p.SupportedProtocols(), peer)
}
