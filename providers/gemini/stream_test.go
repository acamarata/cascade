// Purpose: the streaming-leg unit suite (Stream/emit/decodeGeminiChunk),
// split out of gemini_test.go to hold the 300-line cap (mirrors the
// gemini.go/stream.go production split). Replays testdata/README.md's
// transcribed wire shapes (Art.2.2) against the same recording HTTPDoer
// fake gemini_test.go declares.
// SPORT: placeholder: providers/gemini driver (ADD) - see gemini.go.
package gemini

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const recordedStreamSSE = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}],\"role\":\"model\"}}]}\n\n" +
	"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":2,\"totalTokenCount\":6}}\n\n"

func TestStreamHappyPath(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedStreamSSE}}}
	d := keyAuthDriver(t, doer)
	var deltas []string
	sawDone := false
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
		switch ev.Kind {
		case provider.StreamEventDelta:
			deltas = append(deltas, ev.Delta)
		case provider.StreamEventDone:
			sawDone = true
		case provider.StreamEventUnknown, provider.StreamEventToolCall, provider.StreamEventUsage, provider.StreamEventError: // not asserted here
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !sawDone || len(deltas) != 2 || deltas[0] != "Hi" || deltas[1] != "!" {
		t.Fatalf("deltas=%v done=%v", deltas, sawDone)
	}
}

// Both malformed and truncated chunks are KindIntegrity, never a panic.
func TestStreamMalformedOrTruncatedChunkReportsIntegrityError(t *testing.T) {
	bodies := map[string]string{
		"malformed": "data: not-json\n\n",
		"truncated": "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"cut off",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			doer := &fakeDoer{queue: []fakeResp{{status: 200, body: body}}}
			d := keyAuthDriver(t, doer)
			var sawErrorEvent bool
			req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
			err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
				sawErrorEvent = sawErrorEvent || ev.Kind == provider.StreamEventError
				return nil
			})
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
				t.Fatalf("kind = %v, ok=%v, want KindIntegrity", kind, ok)
			}
			if !sawErrorEvent {
				t.Fatal("sink never received the terminal error event")
			}
		})
	}
}

func TestStreamMidStreamErrorChunkIsTypedAndTerminal(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n\n" +
		"data: {\"error\":{\"code\":429,\"message\":\"slow down\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"never delivered\"}]}}]}\n\n"
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: body}}}
	d := keyAuthDriver(t, doer)
	var terminalCount int
	var deltas []string
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
		if ev.Kind == provider.StreamEventDone || ev.Kind == provider.StreamEventError {
			terminalCount++
		}
		if ev.Kind == provider.StreamEventDelta {
			deltas = append(deltas, ev.Delta)
		}
		return nil
	})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindQuotaExhausted {
		t.Fatalf("kind = %v, ok=%v, want KindQuotaExhausted", kind, ok)
	}
	if terminalCount != 1 {
		t.Fatalf("terminalCount = %d, want exactly 1", terminalCount)
	}
	if len(deltas) != 1 || deltas[0] != "Hi" {
		t.Fatalf("deltas = %v, want exactly the pre-error delta", deltas)
	}
}

func TestStreamNonOKStatus(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 500, body: `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`}}}
	d := keyAuthDriver(t, doer)
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(provider.StreamEvent) error { return nil })
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, ok=%v, want KindUnavailable", kind, ok)
	}
}

// TestStream403MapsToCapabilityDenied asserts the driver's 403 (disabled/
// billing) mapping, added by this ticket alongside the chooser's error
// semantics: distinct from KindPermissionDenied (401) so the chooser
// quarantines the DOMAIN, never a credential.
func TestStream403MapsToCapabilityDenied(t *testing.T) {
	body := `{"error":{"code":403,"message":"billing disabled","status":"PERMISSION_DENIED"}}`
	doer := &fakeDoer{queue: []fakeResp{{status: 403, body: body}}}
	d := keyAuthDriver(t, doer)
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(provider.StreamEvent) error { return nil })
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindCapabilityDenied {
		t.Fatalf("kind = %v, ok=%v, want KindCapabilityDenied", kind, ok)
	}
}

func TestStreamSinkErrorAborts(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedStreamSSE}}}
	d := keyAuthDriver(t, doer)
	sinkErr := errors.New("sink refuses")
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(provider.StreamEvent) error { return sinkErr })
	if !errors.Is(err, sinkErr) {
		t.Fatalf("err = %v, want sinkErr", err)
	}
}

// FuzzGeminiWireDecode fuzzes the decode path; it must never panic.
func FuzzGeminiWireDecode(f *testing.F) {
	for _, seed := range [][]byte{nil, {}, []byte("data: {\n\n"), []byte("data: {\"error\":not-json}\n\n"), []byte("data: {}"), []byte(":\n\n")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decode panicked on %q: %v", in, r)
			}
		}()
		for _, chunk := range scanGeminiSSE(strings.NewReader(string(in))) {
			_, _, _ = decodeGeminiChunk(chunk)
		}
	})
}
