// Purpose: the runnable godoc Example for provider.AgentProvider (Art.10.6)
//
//	and error-path tests over its shape, including the AD/S-61.T1 job
//	dispatch verbs added in place.
//
// Constraints: every double here exists ONLY in this _test.go file
//
//	(Art.1.1); no implementation ships from this ticket — the plugin-hosted
//	AgentProvider driver is O/S-32.T1's wazero host, N/S-29's dispatch, and
//	R/S-40's routing to build. providers/agents/conformance holds the
//	driver-agnostic suite; this file only proves echoAgentProvider still
//	satisfies the widened interface.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// echoAgentProvider is a contract-holding AgentProvider double: Chat echoes
// the last message back, Embed/Count/Stream/Capabilities return fixed
// deterministic shapes, and Spawn/Status/Collect run a trivial synchronous
// job lifecycle (never terminal-async — providers/agents/conformance
// exercises the asynchronous and refusal shapes).
type echoAgentProvider struct {
	mu            sync.Mutex
	jobs          map[provider.AgentJobID]provider.AgentRunState
	next          int
	noEntitlement bool
}

func newEchoAgentProvider() *echoAgentProvider {
	return &echoAgentProvider{jobs: make(map[provider.AgentJobID]provider.AgentRunState)}
}

var _ provider.AgentProvider = newEchoAgentProvider()

func (p *echoAgentProvider) Chat(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return provider.ChatResponse{}, cascade.New(cascade.KindInvalidInput, "chat request has no messages")
	}
	last := req.Messages[len(req.Messages)-1]
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: "echo: " + last.Content},
		FinishReason: "stop",
	}, nil
}

func (p *echoAgentProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = []float32{float32(len(req.Inputs[i]))}
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

func (p *echoAgentProvider) Count(_ context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{Tokens: len(req.Text)}, nil
}

func (p *echoAgentProvider) Stream(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}

func (p *echoAgentProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{
		CompliancePosture: provider.NewCompliancePosture(
			[]string{"api-key"}, false, true, []string{"agent"}, "steady", false,
		),
	}, nil
}

func (p *echoAgentProvider) Spawn(_ context.Context, _ provider.AgentJobSpec) (provider.SpawnResult, error) {
	if p.noEntitlement {
		return provider.SpawnResult{}, provider.ErrEntitlement
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := provider.AgentJobID(fmt.Sprintf("job-%d", p.next))
	p.jobs[id] = provider.AgentRunRunning
	return provider.SpawnResult{JobID: id, ProcessGroupID: 1000 + p.next}, nil
}

func (p *echoAgentProvider) ApprovalRequests() <-chan provider.ApprovalRequest {
	ch := make(chan provider.ApprovalRequest)
	close(ch)
	return ch
}

func (p *echoAgentProvider) ResolveApproval(_ string, _ string) error { return nil }

func (p *echoAgentProvider) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.jobs[id]; !ok {
		return provider.ErrJobNotFound
	}
	return nil
}

func (p *echoAgentProvider) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.jobs[id]
	if !ok {
		return "", provider.ErrJobNotFound
	}
	return s, nil
}

func (p *echoAgentProvider) Cancel(_ context.Context, id provider.AgentJobID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.jobs[id]; !ok {
		return provider.ErrJobNotFound
	}
	p.jobs[id] = provider.AgentRunCancelled
	return nil
}

func (p *echoAgentProvider) Collect(_ context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.jobs[id]; !ok {
		return provider.CollectResult{}, provider.ErrJobNotFound
	}
	p.jobs[id] = provider.AgentRunAccepted
	return provider.CollectResult{JobID: id, DataClass: provider.DataClassRestricted}, nil
}

func (p *echoAgentProvider) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.jobs[id]; !ok {
		return nil, provider.ErrJobNotFound
	}
	return []string{}, nil
}

func (p *echoAgentProvider) SupportedProtocols() provider.ProtocolRange {
	return provider.ProtocolRange{Min: "v1", Max: "v1"}
}

func (p *echoAgentProvider) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(p.SupportedProtocols(), peer)
}

// erroringAgentProvider always fails Chat with a taxonomy error, for the
// error-path test.
type erroringAgentProvider struct{ *echoAgentProvider }

func (erroringAgentProvider) Chat(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, cascade.New(cascade.KindUnavailable, "agent provider offline")
}

func TestAgentProvider_ChatEmptyMessages(t *testing.T) {
	var ap provider.AgentProvider = newEchoAgentProvider()
	_, err := ap.Chat(context.Background(), provider.ChatRequest{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Chat with no messages: err = %v, want KindInvalidInput", err)
	}
}

func TestAgentProvider_ChatError(t *testing.T) {
	var ap provider.AgentProvider = erroringAgentProvider{newEchoAgentProvider()}
	_, err := ap.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Chat error = %v, want KindUnavailable", err)
	}
}

func TestAgentProvider_EmbedCount(t *testing.T) {
	var ap provider.AgentProvider = newEchoAgentProvider()
	ctx := context.Background()

	embedRes, err := ap.Embed(ctx, provider.ModelEmbedRequest{Inputs: []string{"ab", "abcd"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embedRes.Vectors) != 2 || embedRes.Vectors[0][0] != 2 || embedRes.Vectors[1][0] != 4 {
		t.Fatalf("Embed vectors = %v, want lengths [2 4]", embedRes.Vectors)
	}

	countRes, err := ap.Count(ctx, provider.CountRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if countRes.Tokens != 5 {
		t.Fatalf("Count.Tokens = %d, want 5", countRes.Tokens)
	}
}

func TestAgentProvider_SpawnStatusCollect(t *testing.T) {
	ap := newEchoAgentProvider()
	ctx := context.Background()

	res, err := ap.Spawn(ctx, provider.AgentJobSpec{Prompt: "hi", DataClass: provider.DataClassInternal})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if res.ProcessGroupID == 0 {
		t.Fatalf("Spawn: ProcessGroupID = 0, want nonzero for a child-spawning lane")
	}

	state, err := ap.Status(ctx, res.JobID)
	if err != nil || state != provider.AgentRunRunning {
		t.Fatalf("Status = (%v, %v), want (running, nil)", state, err)
	}

	if _, err := ap.Collect(ctx, res.JobID); err != nil {
		t.Fatalf("Collect: %v", err)
	}
}

func TestAgentProvider_SpawnErrEntitlement(t *testing.T) {
	ap := newEchoAgentProvider()
	ap.noEntitlement = true
	_, err := ap.Spawn(context.Background(), provider.AgentJobSpec{})
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("Spawn err = %v, want ErrEntitlement (KindPolicyDenied)", err)
	}
}

func TestAgentProvider_JobNotFound(t *testing.T) {
	ap := newEchoAgentProvider()
	ctx := context.Background()
	if _, err := ap.Status(ctx, "missing"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Status(missing) = %v, want ErrJobNotFound", err)
	}
	if err := ap.Cancel(ctx, "missing"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Cancel(missing) = %v, want ErrJobNotFound", err)
	}
	if _, err := ap.Collect(ctx, "missing"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Collect(missing) = %v, want ErrJobNotFound", err)
	}
}

func TestAgentProvider_NegotiateProtocol(t *testing.T) {
	ap := newEchoAgentProvider()
	got, err := ap.Negotiate(context.Background(), provider.ProtocolRange{Min: "v1", Max: "v2"})
	if err != nil || got != "v1" {
		t.Fatalf("Negotiate = (%v, %v), want (v1, nil)", got, err)
	}
	if _, err := ap.Negotiate(context.Background(), provider.ProtocolRange{Min: "v9", Max: "v9"}); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Negotiate out-of-range err = %v, want ErrHarnessIncompatible", err)
	}
}

// ExampleAgentProvider dispatches a chat exchange through an AgentProvider
// implementation — the same shape a plugin's guest-side driver satisfies
// per pkg/plugin's guest invoke shim (R-14.50).
func ExampleAgentProvider() {
	ctx := context.Background()
	var ap provider.AgentProvider = newEchoAgentProvider()

	res, err := ap.Chat(ctx, provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		fmt.Println("chat error:", err)
		return
	}

	fmt.Println(res.Message.Content)
	// Output: echo: hello
}
