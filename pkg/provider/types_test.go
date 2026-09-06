package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeModelProvider is a minimal ModelProvider used to prove the interface's
// five-verb surface compiles and to exercise its typed error paths. It is
// not a driver and never ships.
type fakeModelProvider struct {
	caps provider.Capabilities
	err  error
}

func (f *fakeModelProvider) Chat(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	if f.err != nil {
		return provider.ChatResponse{}, f.err
	}
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}}, nil
}

func (f *fakeModelProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if f.err != nil {
		return provider.ModelEmbedResponse{}, f.err
	}
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = []float32{1, 2, 3}
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

func (f *fakeModelProvider) Count(_ context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	if f.err != nil {
		return provider.CountResponse{}, f.err
	}
	return provider.CountResponse{Tokens: len(req.Text)}, nil
}

func (f *fakeModelProvider) Stream(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
	if f.err != nil {
		return f.err
	}
	if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hi"}); err != nil {
		return err
	}
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}

func (f *fakeModelProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	if f.err != nil {
		return provider.Capabilities{}, f.err
	}
	return f.caps, nil
}

// TestModelProviderContract is the conformance test: compile-time
// satisfaction of the five-verb surface (assigning *fakeModelProvider to a
// provider.ModelProvider variable fails to compile if the surface drifts),
// capabilities/requirements type behavior, and the typed models' error
// paths.
// newContractFakeProvider builds the fakeModelProvider TestModelProviderContract
// exercises, factored out to keep that test under the funlen line cap.
func newContractFakeProvider() provider.ModelProvider {
	return &fakeModelProvider{
		caps: provider.Capabilities{
			Search:            provider.CapabilitySupported,
			ToolUse:           provider.CapabilitySupported,
			LongContext:       provider.CapabilityUnknown,
			StructuredOutput:  provider.CapabilityUnsupported,
			CompliancePosture: provider.NewCompliancePosture(nil, false, true, nil, "steady", false),
		},
	}
}

func TestModelProviderContract(t *testing.T) {
	mp := newContractFakeProvider()
	ctx := context.Background()

	chatResp, err := mp.Chat(ctx, provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if chatResp.Message.Content != "ok" {
		t.Fatalf("Chat() message = %q, want ok", chatResp.Message.Content)
	}

	embedResp, err := mp.Embed(ctx, provider.ModelEmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(embedResp.Vectors) != 2 {
		t.Fatalf("Embed() returned %d vectors, want 2", len(embedResp.Vectors))
	}

	countResp, err := mp.Count(ctx, provider.CountRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if countResp.Tokens != 5 {
		t.Fatalf("Count() = %d, want 5", countResp.Tokens)
	}

	testModelProviderContractStreamAndCapabilities(ctx, t, mp)
}

// testModelProviderContractStreamAndCapabilities covers the Stream and
// Capabilities legs of the five-verb conformance test, factored out of
// TestModelProviderContract to keep it under the funlen line cap.
func testModelProviderContractStreamAndCapabilities(ctx context.Context, t *testing.T, mp provider.ModelProvider) {
	t.Helper()

	var events []provider.StreamEvent
	if err := mp.Stream(ctx, provider.ChatRequest{}, func(ev provider.StreamEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(events) != 2 || events[1].Kind != provider.StreamEventDone {
		t.Fatalf("Stream() events = %+v, want [delta, done]", events)
	}

	caps, err := mp.Capabilities(ctx, "")
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if caps.Search != provider.CapabilitySupported {
		t.Fatalf("Capabilities().Search = %v, want supported", caps.Search)
	}
	if err := caps.CompliancePosture.Validate(); err != nil {
		t.Fatalf("CompliancePosture.Validate() error = %v", err)
	}
}

// TestModelProviderTypedErrorPaths asserts a taxonomy error returned by a
// driver's implementation of each verb survives the interface boundary
// unchanged and is still recoverable via cascade.KindOf.
func TestModelProviderTypedErrorPaths(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "provider unreachable")
	var mp provider.ModelProvider = &fakeModelProvider{err: wantErr}
	ctx := context.Background()

	if _, err := mp.Chat(ctx, provider.ChatRequest{}); !errors.Is(err, wantErr) {
		t.Fatalf("Chat() error = %v, want %v", err, wantErr)
	}
	if _, err := mp.Embed(ctx, provider.ModelEmbedRequest{}); !errors.Is(err, wantErr) {
		t.Fatalf("Embed() error = %v, want %v", err, wantErr)
	}
	if _, err := mp.Count(ctx, provider.CountRequest{}); !errors.Is(err, wantErr) {
		t.Fatalf("Count() error = %v, want %v", err, wantErr)
	}
	if err := mp.Stream(ctx, provider.ChatRequest{}, func(provider.StreamEvent) error { return nil }); !errors.Is(err, wantErr) {
		t.Fatalf("Stream() error = %v, want %v", err, wantErr)
	}
	if _, err := mp.Capabilities(ctx, ""); !errors.Is(err, wantErr) {
		t.Fatalf("Capabilities() error = %v, want %v", err, wantErr)
	}
	if kind, ok := cascade.KindOf(wantErr); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("KindOf(wantErr) = %v, %v, want KindUnavailable, true", kind, ok)
	}
}

func TestCapabilityStateString(t *testing.T) {
	cases := []struct {
		state provider.CapabilityState
		want  string
	}{
		{provider.CapabilityUnknown, "unknown"},
		{provider.CapabilitySupported, "supported"},
		{provider.CapabilityUnsupported, "unsupported"},
		{provider.CapabilityState(200), "invalid-capability-state"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("CapabilityState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestCapabilitiesSatisfies(t *testing.T) {
	caps := provider.Capabilities{
		Search:  provider.CapabilitySupported,
		ToolUse: provider.CapabilityUnsupported,
	}

	if !caps.Satisfies(provider.RequiredCapabilities{Search: true}) {
		t.Fatal("Satisfies should be true when the only required dimension is supported")
	}
	if caps.Satisfies(provider.RequiredCapabilities{ToolUse: true}) {
		t.Fatal("Satisfies should be false when a required dimension is unsupported")
	}
	if caps.Satisfies(provider.RequiredCapabilities{Vision: true}) {
		t.Fatal("Satisfies should be false when a required dimension is unknown")
	}
	if !caps.Satisfies(provider.RequiredCapabilities{}) {
		t.Fatal("Satisfies should be true when nothing is required")
	}
}
