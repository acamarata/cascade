package preauth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreAuthInvalidInput(t *testing.T) {
	calls := 0
	fakeVerifier := func(req Request) error {
		calls++
		return nil
	}
	fakeClock := func() time.Time {
		calls++
		return time.Now()
	}

	p, err := New(10, fakeVerifier, fakeClock)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	tests := []struct {
		name      string
		req       Request
		wantCode  int
		wantErr   error
		wantCalls int
	}{
		{
			name: "empty source",
			req: Request{
				Source:    "",
				Body:      []byte("body"),
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "whitespace only source - space",
			req: Request{
				Source:    "   ",
				Body:      []byte("body"),
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "whitespace only source - tabs and newlines",
			req: Request{
				Source:    "\t\n\r  ",
				Body:      []byte("body"),
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "whitespace only source - unicode whitespace",
			req: Request{
				Source:    "\u00A0\u2003\u3000",
				Body:      []byte("body"),
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "nil body",
			req: Request{
				Source:    "valid-source",
				Body:      nil,
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "empty body",
			req: Request{
				Source:    "valid-source",
				Body:      []byte{},
				Signature: []byte("sig"),
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "nil signature",
			req: Request{
				Source:    "valid-source",
				Body:      []byte("body"),
				Signature: nil,
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name: "empty signature",
			req: Request{
				Source:    "valid-source",
				Body:      []byte("body"),
				Signature: []byte{},
			},
			wantCode:  400,
			wantErr:   ErrInvalidRequest,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls = 0
			code, err := p.Handle(tt.req)
			if code != tt.wantCode {
				t.Errorf("got code %d, want %d", code, tt.wantCode)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got err %v, want %v", err, tt.wantErr)
			}
			if calls != tt.wantCalls {
				t.Errorf("got %d callback calls, want %d", calls, tt.wantCalls)
			}
		})
	}

	// Verify nonblank source strings are keyed verbatim
	t.Run("nonblank source is keyed verbatim", func(t *testing.T) {
		sources := []string{"source1", " source2 ", "source\twith\ttabs"}
		for _, src := range sources {
			calls = 0
			req := Request{
				Source:    src,
				Body:      []byte("body"),
				Signature: []byte("sig"),
			}
			_, _ = p.Handle(req)
			// Should call verifier (not return early with 400)
			if calls == 0 {
				t.Errorf("source %q was not keyed verbatim (verifier not called)", src)
			}
		}
	})
}

func TestPreAuthInvalidConfig(t *testing.T) {
	fakeVerifier := func(req Request) error { return nil }
	fakeClock := func() time.Time { return time.Now() }

	// Test invalid config cases
	configTests := []struct {
		name     string
		limit    int
		verifier Verifier
		clock    Clock
		wantErr  error
	}{
		{
			name:     "zero limit",
			limit:    0,
			verifier: fakeVerifier,
			clock:    fakeClock,
			wantErr:  ErrInvalidConfig,
		},
		{
			name:     "negative limit",
			limit:    -1,
			verifier: fakeVerifier,
			clock:    fakeClock,
			wantErr:  ErrInvalidConfig,
		},
		{
			name:     "nil verifier",
			limit:    10,
			verifier: nil,
			clock:    fakeClock,
			wantErr:  ErrInvalidConfig,
		},
		{
			name:     "nil clock",
			limit:    10,
			verifier: fakeVerifier,
			clock:    nil,
			wantErr:  ErrInvalidConfig,
		},
		{
			name:     "nil verifier and clock",
			limit:    10,
			verifier: nil,
			clock:    nil,
			wantErr:  ErrInvalidConfig,
		},
	}

	for _, tt := range configTests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			var v Verifier
			if tt.verifier != nil {
				v = func(req Request) error {
					called = true
					return tt.verifier(req)
				}
			}
			var c Clock
			if tt.clock != nil {
				c = func() time.Time {
					called = true
					return tt.clock()
				}
			}

			p, err := New(tt.limit, v, c)
			if p != nil {
				t.Error("expected nil PreAuth")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got err %v, want %v", err, tt.wantErr)
			}
			if called {
				t.Error("callback should not be invoked for invalid config")
			}
		})
	}

	// Test nil receiver
	t.Run("nil receiver returns 500 ErrInvalidConfig", func(t *testing.T) {
		var p *PreAuth
		req := Request{
			Source:    "source",
			Body:      []byte("body"),
			Signature: []byte("sig"),
		}
		code, err := p.Handle(req)
		if code != 500 {
			t.Errorf("got code %d, want 500", code)
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got err %v, want %v", err, ErrInvalidConfig)
		}
	})

	// Test zero-value PreAuth
	t.Run("zero-value PreAuth returns 500 ErrInvalidConfig", func(t *testing.T) {
		var p PreAuth
		req := Request{
			Source:    "source",
			Body:      []byte("body"),
			Signature: []byte("sig"),
		}
		code, err := p.Handle(req)
		if code != 500 {
			t.Errorf("got code %d, want 500", code)
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got err %v, want %v", err, ErrInvalidConfig)
		}
	})

	// Test sentinel messages
	t.Run("sentinel messages match objective", func(t *testing.T) {
		sentinelTests := []struct {
			err     error
			message string
		}{
			{ErrInvalidConfig, "preauth: invalid config"},
			{ErrInvalidRequest, "preauth: invalid request"},
			{ErrInvalidSignature, "preauth: invalid signature"},
			{ErrRateLimited, "preauth: rate limited"},
		}
		for _, st := range sentinelTests {
			if st.err.Error() != st.message {
				t.Errorf("error %v has message %q, want %q", st.err, st.err.Error(), st.message)
			}
			if !errors.Is(st.err, st.err) {
				t.Errorf("errors.Is(%v, %v) returned false", st.err, st.err)
			}
		}
	})
}

// Ensure we use strings package for whitespace trimming
var _ = strings.TrimSpace
