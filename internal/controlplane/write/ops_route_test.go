package write

import (
	"context"
	"errors"
	"testing"
)

func TestRouteHookDist(t *testing.T) {
	called := 0
	var gotCtx context.Context
	var gotPayload []byte
	ctx := context.WithValue(context.Background(), "key", "val")
	payload := []byte("data")

	handlers := Handlers{
		"hook-dist": func(c context.Context, p []byte) error {
			called++
			gotCtx = c
			gotPayload = p
			return nil
		},
		"hook-run": func(c context.Context, p []byte) error {
			t.Fatal("hook-run called")
			return nil
		},
	}

	err := Route(ctx, "hook-dist", payload, handlers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected 1 call, got %d", called)
	}
	if gotCtx != ctx {
		t.Fatal("context not identical")
	}
	if string(gotPayload) != string(payload) {
		t.Fatal("payload not identical")
	}

	// Test with nil payload
	called = 0
	err = Route(ctx, "hook-dist", nil, handlers)
	if err != nil {
		t.Fatalf("unexpected error with nil payload: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected 1 call with nil payload, got %d", called)
	}

	// Test with empty payload
	called = 0
	err = Route(ctx, "hook-dist", []byte{}, handlers)
	if err != nil {
		t.Fatalf("unexpected error with empty payload: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected 1 call with empty payload, got %d", called)
	}
}

func TestRouteUnknownOpFailsClosed(t *testing.T) {
	ctx := context.Background()
	payload := []byte("data")

	called := 0
	dummyHandler := func(c context.Context, p []byte) error {
		called++
		return nil
	}

	tests := []struct {
		name     string
		op       string
		ctx      context.Context
		handlers Handlers
	}{
		{"unknown op", "unknown", ctx, Handlers{"hook-dist": dummyHandler}},
		{"empty op", "", ctx, Handlers{"hook-dist": dummyHandler}},
		{"padded op", " hook-dist ", ctx, Handlers{"hook-dist": dummyHandler}},
		{"nil map", "hook-dist", ctx, nil},
		{"empty map", "hook-dist", ctx, Handlers{}},
		{"missing handler", "hook-dist", ctx, Handlers{"hook-run": dummyHandler}},
		{"nil handler", "hook-dist", ctx, Handlers{"hook-dist": nil}},
		{"extra unknown registered op", "unknown-op", ctx, Handlers{"unknown-op": dummyHandler, "hook-dist": dummyHandler}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = 0
			err := Route(tt.ctx, tt.op, payload, tt.handlers)
			if !errors.Is(err, ErrUnknownOp) {
				t.Fatalf("expected ErrUnknownOp, got %v", err)
			}
			if err.Error() != "E_UNKNOWN_OP" {
				t.Fatalf("expected error text 'E_UNKNOWN_OP', got %q", err.Error())
			}
			if called != 0 {
				t.Fatalf("expected 0 calls, got %d", called)
			}
		})
	}

	// Valid selection with nil ctx
	t.Run("valid selection nil ctx", func(t *testing.T) {
		called = 0
		err := Route(nil, "hook-dist", payload, Handlers{"hook-dist": dummyHandler})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("expected ErrInvalidRoute, got %v", err)
		}
		if err.Error() != "invalid route context" {
			t.Fatalf("expected error text 'invalid route context', got %q", err.Error())
		}
		if called != 0 {
			t.Fatalf("expected 0 calls, got %d", called)
		}
	})

	// Invalid selection takes precedence over nil ctx
	t.Run("invalid selection precedence over nil ctx", func(t *testing.T) {
		called = 0
		err := Route(nil, "unknown", payload, Handlers{"hook-dist": dummyHandler})
		if !errors.Is(err, ErrUnknownOp) {
			t.Fatalf("expected ErrUnknownOp, got %v", err)
		}
		if err.Error() != "E_UNKNOWN_OP" {
			t.Fatalf("expected error text 'E_UNKNOWN_OP', got %q", err.Error())
		}
		if called != 0 {
			t.Fatalf("expected 0 calls, got %d", called)
		}
	})
}
