package provider_test

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestStreamEventKindZeroValueIsUnknown(t *testing.T) {
	var kind provider.StreamEventKind
	if kind != provider.StreamEventUnknown {
		t.Fatalf("zero value = %v, want StreamEventUnknown", kind)
	}
	if got, want := kind.String(), "unknown"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStreamEventKindString(t *testing.T) {
	cases := []struct {
		kind provider.StreamEventKind
		want string
	}{
		{provider.StreamEventUnknown, "unknown"},
		{provider.StreamEventDelta, "delta"},
		{provider.StreamEventToolCall, "tool_call"},
		{provider.StreamEventUsage, "usage"},
		{provider.StreamEventDone, "done"},
		{provider.StreamEventError, "error"},
		{provider.StreamEventKind(200), "invalid-stream-event-kind"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("StreamEventKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestStreamEventKindValid(t *testing.T) {
	if !provider.StreamEventError.Valid() {
		t.Fatal("StreamEventError should be valid")
	}
	if provider.StreamEventKind(200).Valid() {
		t.Fatal("StreamEventKind(200) should be invalid")
	}
}

func TestStreamEventCarriesToolCall(t *testing.T) {
	ev := provider.StreamEvent{
		Kind: provider.StreamEventToolCall,
		ToolCall: provider.ToolCall{
			Name:      "lookup",
			Arguments: map[string]any{"query": "cascade"},
		},
	}
	if ev.ToolCall.Name != "lookup" {
		t.Fatalf("ToolCall.Name = %q, want lookup", ev.ToolCall.Name)
	}
	if ev.ToolCall.Arguments["query"] != "cascade" {
		t.Fatalf("ToolCall.Arguments[query] = %v, want cascade", ev.ToolCall.Arguments["query"])
	}
}

func TestStreamEventCarriesErr(t *testing.T) {
	wantErr := errors.New("boom")
	ev := provider.StreamEvent{Kind: provider.StreamEventError, Err: wantErr}
	if !errors.Is(ev.Err, wantErr) {
		t.Fatalf("Err = %v, want %v", ev.Err, wantErr)
	}
}

func TestStreamEventCarriesUsage(t *testing.T) {
	ev := provider.StreamEvent{
		Kind:  provider.StreamEventUsage,
		Usage: provider.Usage{InputTokens: 10, OutputTokens: 4},
	}
	if ev.Usage.InputTokens != 10 || ev.Usage.OutputTokens != 4 {
		t.Fatalf("Usage = %+v, want {10 4}", ev.Usage)
	}
}
