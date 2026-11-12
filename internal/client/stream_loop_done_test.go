package client

// Purpose: pin streamLoop's send-side cancellation branch deterministically.
// A real cancel can land before or after the loop's ctx.Err check, so the
// branch was covered only by timing; this context has Done closed while Err
// still reports nil, which reaches the select every run.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// doneOnlyContext reports no error but has a closed Done channel.
type doneOnlyContext struct {
	context.Context
	done chan struct{}
}

func (c doneOnlyContext) Done() <-chan struct{} { return c.done }
func (c doneOnlyContext) Err() error            { return nil }

// TestStreamLoopStopsWhenDoneWhileSending: a completed event with no reader
// and a closed Done ends the loop through the send select and closes out.
func TestStreamLoopStopsWhenDoneWhileSending(t *testing.T) {
	ctx := doneOnlyContext{Context: context.Background(), done: make(chan struct{})}
	close(ctx.done)
	out := make(chan sseEvent)
	finished := make(chan struct{})
	go func() {
		streamLoop(ctx, strings.NewReader("data: x\n\n"), out)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("streamLoop did not return on a closed Done")
	}
	if _, open := <-out; open {
		t.Fatal("out delivered an event after Done; want closed")
	}
}
