package conductor

// Purpose (this file): TestDoorCanaryNeverLeaves - a vault canary sent through
//   each of the six doors reaches a recording provider only as its tag. A leaf
//   door that skips enterDoor (the HEAD path: Ready() then dispatch) leaks it.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

// Synthetic and shapeless: long enough for the exact-substring pass (8
// bytes) and carrying no credential shape, so only the vault match can
// catch it.
const (
	canaryName  = "door-canary-key"
	canaryValue = "zq7-canary-door-4471"
	// canaryTag is how the rewriter names the stored value.
	canaryTag = "<apikey>DOOR_CANARY_KEY</apikey>"
)

type canaryVault struct{}

func (canaryVault) List(context.Context) ([]string, error) { return []string{canaryName}, nil }
func (canaryVault) Get(context.Context, string) ([]byte, error) {
	return []byte(canaryValue), nil
}

// seenText is everything a recording provider received, in call order.
type seenText struct {
	mu    sync.Mutex
	parts []string
}

func (s *seenText) add(parts ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts = append(s.parts, parts...)
}

func (s *seenText) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.parts, "\n")
}

func chatTexts(msgs []provider.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

// newCanaryExecutor builds a ready executor over a real egress engine whose
// vault holds the canary, with every provider verb recording what it got.
func newCanaryExecutor(t *testing.T) (*Executor, *seenText) {
	t.Helper()
	return newCanaryExecutorFor(t, canaryVault{})
}

// newCanaryExecutorFor is newCanaryExecutor over a caller-chosen vault.
func newCanaryExecutorFor(t *testing.T, vault egress.Vault) (*Executor, *seenText) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, deps := newReadyConfig(t)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	if cfg.Firewall, err = egress.NewEngine(egress.DefaultRegistry(), vault, detector); err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	seen := &seenText{}
	deps.prov.chatFn = func(_ context.Context, r provider.ChatRequest) (provider.ChatResponse, error) {
		seen.add(append(chatTexts(r.Messages), r.Model)...)
		return provider.ChatResponse{}, nil
	}
	deps.prov.streamFn = func(_ context.Context, r provider.ChatRequest, sink provider.StreamSink) error {
		seen.add(append(chatTexts(r.Messages), r.Model)...)
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	deps.prov.embedFn = func(_ context.Context, r provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		seen.add(append(r.Inputs, r.Model)...)
		return provider.ModelEmbedResponse{}, nil
	}
	deps.prov.countFn = func(_ context.Context, r provider.CountRequest) (provider.CountResponse, error) {
		seen.add(r.Text, r.Model)
		return provider.CountResponse{}, nil
	}
	return exec, seen
}

func TestDoorCanaryNeverLeaves(t *testing.T) {
	probe := "token is " + canaryValue + " ok"
	msgs := []provider.ChatMessage{{Role: "user", Content: probe}}
	sel := provider.Selection{LaneID: "lane-1"}
	ctx := context.Background()
	doors := map[string]func(*Executor) error{
		"Execute": func(e *Executor) error {
			_, err := e.Execute(ctx, provider.ModelRequest{TaskID: "t", TaskClass: "chat", Inputs: msgs})
			return err
		},
		"ExecuteStream": func(e *Executor) error {
			ch, _, err := e.ExecuteStream(ctx, provider.ModelRequest{TaskID: "t", TaskClass: "chat", Inputs: msgs})
			for range ch {
				continue
			}
			return err
		},
		"Chat": func(e *Executor) error {
			_, err := e.Chat(ctx, sel, provider.ChatRequest{Messages: msgs})
			return err
		},
		"Embed": func(e *Executor) error {
			_, err := e.Embed(ctx, sel, provider.ModelEmbedRequest{Inputs: []string{probe}})
			return err
		},
		"Count": func(e *Executor) error {
			_, err := e.Count(ctx, sel, provider.CountRequest{Text: probe})
			return err
		},
		"Stream": func(e *Executor) error {
			return e.Stream(ctx, sel, provider.ChatRequest{Messages: msgs}, func(provider.StreamEvent) error { return nil })
		},
	}
	for name, call := range doors {
		t.Run(name, func(t *testing.T) {
			exec, seen := newCanaryExecutor(t)
			if err := call(exec); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got := seen.joined()
			if got == "" {
				t.Fatalf("%s: the recording provider was never called; the test would prove nothing", name)
			}
			if strings.Contains(got, canaryValue) {
				t.Fatalf("%s: the vault canary reached the provider: %q", name, got)
			}
			if !strings.Contains(got, canaryTag) {
				t.Fatalf("%s: the provider did not receive the vault tag: %q", name, got)
			}
		})
	}
}
