// Package write provides control plane write operations.
package write

import (
	"context"
	"errors"
	"testing"
)

// TestRouteFlowRun verifies that flow-run dispatches once to its own handler,
// preserves context/payload, and returns the handler error unchanged with no retry.
func TestRouteFlowRun(t *testing.T) {
	ctx := context.Background()
	payload := []byte("test-payload-flow-run")

	var callCount int
	var gotCtx context.Context
	var gotPayload []byte

	handlerErr := errors.New("flow-run-handler-error")

	handlers := Handlers{
		"flow-run": func(c context.Context, p []byte) error {
			callCount++
			gotCtx = c
			gotPayload = p
			return handlerErr
		},
	}

	// Call Route with flow-run operation
	err := Route(ctx, "flow-run", payload, handlers)

	// Verify handler was called exactly once
	if callCount != 1 {
		t.Errorf("handler called %d times, want 1", callCount)
	}

	// Verify context was preserved
	if gotCtx != ctx {
		t.Errorf("context not preserved")
	}

	// Verify payload was preserved
	if string(gotPayload) != string(payload) {
		t.Errorf("payload not preserved: got %v, want %v", gotPayload, payload)
	}

	// Verify error is returned unchanged
	if err != handlerErr {
		t.Errorf("error not unchanged: got %v, want %v", err, handlerErr)
	}
}

// TestRouteExistingOpsUnchanged verifies that hook-run, shelf, sysone and decide
// each dispatch exactly once to their corresponding injected handler.
// Unselected missing handlers are allowed and the dispatch table remains unchanged.
func TestRouteExistingOpsUnchanged(t *testing.T) {
	ops := []string{"hook-run", "shelf", "sysone", "decide"}

	for _, op := range ops {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			payload := []byte("test-payload-" + op)

			var callCount int
			var gotCtx context.Context
			var gotPayload []byte

			handlerErr := errors.New(op + "-handler-error")

			// Create handlers with only the current op
			handlers := Handlers{
				op: func(c context.Context, p []byte) error {
					callCount++
					gotCtx = c
					gotPayload = p
					return handlerErr
				},
			}

			// Call Route with the current operation
			err := Route(ctx, op, payload, handlers)

			// Verify handler was called exactly once
			if callCount != 1 {
				t.Errorf("%s handler called %d times, want 1", op, callCount)
			}

			// Verify context was preserved
			if gotCtx != ctx {
				t.Errorf("%s context not preserved", op)
			}

			// Verify payload was preserved
			if string(gotPayload) != string(payload) {
				t.Errorf("%s payload not preserved: got %v, want %v", op, gotPayload, payload)
			}

			// Verify error is returned unchanged
			if err != handlerErr {
				t.Errorf("%s error not unchanged: got %v, want %v", op, err, handlerErr)
			}
		})
	}
}
