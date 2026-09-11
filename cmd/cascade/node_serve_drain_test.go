package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestDrainOrFail pins BOTH branches, because the whole value of this
// predicate is what it refuses to swallow. A version that returned nil
// whenever ctx was done would pass a "cancelled" test and silently hide
// every real startup failure that happened to race a shutdown.
func TestDrainOrFail(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	realErr := errors.New("keystore unreadable")

	cases := []struct {
		name    string
		ctx     context.Context
		err     error
		wantNil bool
	}{
		{
			name:    "cancelled ctx with the cancellation itself is a clean drain",
			ctx:     canceled,
			err:     fmt.Errorf("sqlite: schema init: %w", context.Canceled),
			wantNil: true,
		},
		{
			name:    "cancelled ctx with an UNRELATED error must still fail",
			ctx:     canceled,
			err:     realErr,
			wantNil: false,
		},
		{
			name:    "live ctx always propagates",
			ctx:     live,
			err:     realErr,
			wantNil: false,
		},
		{
			name:    "live ctx propagates even a context.Canceled-wrapping error",
			ctx:     live,
			err:     fmt.Errorf("wrapped: %w", context.Canceled),
			wantNil: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := drainOrFail(tc.ctx, tc.err)
			if tc.wantNil && got != nil {
				t.Fatalf("drainOrFail = %v, want nil", got)
			}
			if !tc.wantNil && got == nil {
				t.Fatal("drainOrFail = nil, want the error propagated (a swallowed startup failure)")
			}
		})
	}
}
