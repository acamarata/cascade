package cmd

// Purpose (this file): `cascade chat --private` / `--local-only` end to
//   end through the real cobra command — the flag an operator types
//   reaching the OneShotRequest the client sends, and the two flags
//   together refusing.
//
// SPORT: plugins/cascade-pa:cmd:chat privacy flags (TEST) — P1-E20-W5-S44-T2.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// runChatCommand executes the REAL command with argv, capturing the
// OneShotRequest the client was handed. Driving cobra rather than calling
// resolveChatPrivacy directly is the point: a flag that is resolved
// correctly but never registered would pass a unit test of the resolver
// and fail for every operator.
func runChatCommand(t *testing.T, argv ...string) (OneShotRequest, error) {
	t.Helper()
	var got OneShotRequest
	SetClient(fakeClient{
		OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
			got = req
			return OneShotResult{ThreadID: "th-1", TurnID: "tu-1", Content: "ok"}, nil
		},
	})
	t.Cleanup(func() { SetClient(nil) })

	c := NewChatCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	c.SetArgs(argv)
	return got, c.Execute()
}

// TestChatPrivacyFlags is the contract's named check.
func TestChatPrivacyFlags(t *testing.T) {
	t.Run("--local-only sends the local-only tier", func(t *testing.T) {
		got, err := runChatCommand(t, "--local-only", "-m", "hello")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Privacy != chatPrivacyLocalOnly {
			t.Fatalf("Privacy = %q, want %q", got.Privacy, chatPrivacyLocalOnly)
		}
	})

	t.Run("--private sends the restricted tier", func(t *testing.T) {
		got, err := runChatCommand(t, "--private", "-m", "hello")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Privacy != chatPrivacyRestricted {
			t.Fatalf("Privacy = %q, want %q", got.Privacy, chatPrivacyRestricted)
		}
	})

	t.Run("neither flag sends nothing, leaving the daemon default", func(t *testing.T) {
		got, err := runChatCommand(t, "-m", "hello")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		// "" and not "restricted": the fail-closed default is the daemon's
		// to apply, and an explicit tier here would make every thread look
		// like one an operator deliberately marked.
		if got.Privacy != "" {
			t.Fatalf("Privacy = %q, want empty", got.Privacy)
		}
	})

	t.Run("both flags are a usage error and send nothing", func(t *testing.T) {
		got, err := runChatCommand(t, "--private", "--local-only", "-m", "hello")
		if err == nil {
			t.Fatal("Execute returned nil; a command naming two privacy modes succeeded")
		}
		if !errors.Is(err, errChatPrivacyFlagsConflict) {
			t.Fatalf("err = %v, want errChatPrivacyFlagsConflict", err)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("err = %v, want KindInvalidInput", err)
		}
		for _, want := range []string{"--private", "--local-only"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not name %s", err.Error(), want)
			}
		}
		// The refusal must happen BEFORE the turn is sent. A thread
		// created under a mode the operator did not choose is the exact
		// outcome the usage error exists to prevent.
		if got.Prompt != "" {
			t.Fatalf("the client was called with %+v; a conflicting command still sent its turn", got)
		}
	})
}

// TestChatPrivacyFlagsResolve covers the resolver's cells directly,
// including the pairing cobra cannot produce through argv.
func TestChatPrivacyFlagsResolve(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      chatOptions
		want      string
		wantError bool
	}{
		{"neither", chatOptions{}, "", false},
		{"private", chatOptions{private: true}, chatPrivacyRestricted, false},
		{"local-only", chatOptions{localOnly: true}, chatPrivacyLocalOnly, false},
		{"both", chatOptions{private: true, localOnly: true}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveChatPrivacy(tc.opts)
			if tc.wantError {
				if err == nil {
					t.Fatal("resolveChatPrivacy returned nil error, want a conflict")
				}
				if got != "" {
					t.Fatalf("resolveChatPrivacy returned %q with an error; a refused command must name no tier", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveChatPrivacy: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
