// Purpose: frame-level error coverage for forwardReply and the streamReply
//
//	completion race. Ordering is forced with channels (a frame send returns
//	only once forwardReply has received it), never with sleeps.
//
// SPORT: internal/plugins:cascadepa-wiring (TEST) -- FIX-cascade-chat-client-wiring.
package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

func forwardWith(t *testing.T, data string) error {
	t.Helper()
	frames := make(chan client.Event, 1)
	frames <- client.Event{Data: data}
	return forwardReply(context.Background(), frames, make(chan string, 1), make(chan error))
}

func TestForwardReplyFrameErrors(t *testing.T) {
	cases := []struct {
		name, data, want string
		kind             cascade.Kind
	}{
		{"invalid json", `{not json`, "invalid reply frame", cascade.KindIntegrity},
		{"error frame", `{"type":"error","kind":"quota"}`, "reply failed: quota", cascade.KindUnavailable},
		{"unknown frame", `{"type":"mystery"}`, "unknown reply frame", cascade.KindIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := forwardWith(t, tc.data)
			if !cascade.HasKind(err, tc.kind) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.kind, tc.want)
			}
		})
	}
}

func TestForwardReplyStreamEndsBeforeTerminalFrame(t *testing.T) {
	frames := make(chan client.Event)
	close(frames)
	err := forwardReply(context.Background(), frames, nil, make(chan error))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "ended before terminal frame") {
		t.Fatalf("err = %v", err)
	}
}

func TestForwardReplyCompletionSuccessThenDone(t *testing.T) {
	frames := make(chan client.Event)
	completed := make(chan error)
	// The unbuffered send returns only once forwardReply consumed the nil
	// result, so the done frame can never be received first.
	go func() {
		completed <- nil
		frames <- client.Event{Data: `{"type":"done"}`}
	}()
	if err := forwardReply(context.Background(), frames, nil, completed); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestForwardReplyCanceledWhileSendingDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	frames := make(chan client.Event)
	result := make(chan error, 1)
	go func() { result <- forwardReply(ctx, frames, make(chan string), make(chan error)) }()
	frames <- client.Event{Data: `{"type":"delta","delta":"x"}`}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

// streamAfterDone runs Stream so chat.reply blocks until the done frame was
// consumed, then lets after() decide how the completion resolves.
func streamAfterDone(t *testing.T, after func(gate chan struct{}, cancel context.CancelFunc)) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := make(chan struct{})
	d := &subRPC{frames: make(chan client.Event)}
	d.hook = func(ctx context.Context, m string) (bool, error) {
		if m != "chat.reply" {
			return false, nil
		}
		select {
		case <-gate:
			return true, errBoom
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	tokens, errs := (&cascadePAClient{doer: d}).Stream(ctx, pacmd.OneShotRequest{Prompt: "hi"})
	d.frames <- client.Event{Data: `{"type":"done"}`}
	after(gate, cancel)
	for range tokens {
		continue
	}
	return <-errs
}

func TestCascadePAStreamCompletionErrorAfterDone(t *testing.T) {
	err := streamAfterDone(t, func(gate chan struct{}, _ context.CancelFunc) { close(gate) })
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCascadePAStreamCanceledAfterDone(t *testing.T) {
	err := streamAfterDone(t, func(_ chan struct{}, cancel context.CancelFunc) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}
