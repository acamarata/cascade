//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestMCPInboxWithoutPrincipalRefuses checks refusal and bound-token forwarding.
func TestMCPInboxWithoutPrincipalRefuses(t *testing.T) {
	paths, remote := inboxDaemonFixture(t)
	for _, token := range []string{"", fakeToken} {
		t.Setenv(principal.EnvToken, token)
		if token == "" {
			if err := os.Unsetenv(principal.EnvToken); err != nil {
				t.Fatal(err)
			}
		}
		local := rpc.NewRegistry()
		registerMCPInboxMethods(local, paths)
		// A later environment change must never switch forwarding to owner mode.
		t.Setenv(principal.EnvToken, "")
		tools := mcp.NewToolRegistry(noBuiltinTools, grantEverything{}, coretools.Registrations(local)...)
		for name, input := range map[string]string{
			"cascade_inbox_list": `{}`, "cascade_inbox_read": `{"id":"notification"}`,
		} {
			out, err := tools.Call(context.Background(), name, []byte(input))
			if token == "" {
				if !errors.Is(err, cascade.ErrPermissionDenied) || !strings.Contains(err.Error(), "caller principal") {
					t.Fatalf("%s: got %v, want caller principal permission denial", name, err)
				}
				if remote.count("") != 0 {
					t.Fatal("tokenless inbox call reached daemon")
				}
			} else if err != nil || !strings.Contains(string(out), daemonName) {
				t.Fatalf("%s: got %s, %v, want daemon response", name, out, err)
			}
		}
	}
	if remote.count("inbox.list") != 1 || remote.count("inbox.read") != 1 {
		t.Fatal("both inbox reads must reach daemon exactly once")
	}
	for _, seen := range remote.all() {
		if !seen.ok || seen.caller.Principal == nil || seen.caller.OwnerCLI {
			t.Fatal("daemon did not receive a principal-only caller")
		}
	}
}

// TestMCPInboxListRejectsAllAndScope checks that widening inputs stay local.
func TestMCPInboxListRejectsAllAndScope(t *testing.T) {
	paths, remote := inboxDaemonFixture(t)
	t.Setenv(principal.EnvToken, fakeToken)
	local := rpc.NewRegistry()
	registerMCPInboxMethods(local, paths)
	// Exercise the principal-mode replacement used by the serving process too.
	if err := applyPrincipalMode(local, fixedEnv(fakeToken), dialMCPDaemon(paths)); err != nil {
		t.Fatal(err)
	}
	tools := mcp.NewToolRegistry(noBuiltinTools, grantEverything{}, coretools.Registrations(local)...)
	for _, input := range []string{`{"all":true}`, `{"all":false}`, `{"scope":"project:other"}`, `{"ack":true}`} {
		_, err := tools.Call(context.Background(), "cascade_inbox_list", []byte(input))
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: got %v, want schema error", input, err)
		}
	}
	if remote.count("") != 0 {
		t.Fatal("invalid inbox arguments reached daemon")
	}
	if _, err := tools.Call(context.Background(), "cascade_inbox_list", []byte(`{"unread":true}`)); err != nil {
		t.Fatal(err)
	}
	if remote.count("inbox.list") != 1 || local.Registered("inbox.ack") {
		t.Fatal("expected one valid list call and no ack registration")
	}
}

// inboxDaemonFixture uses only a test-owned directory and the real RPC handler.
func inboxDaemonFixture(t *testing.T) (fakeDaemonPaths, *recorder) {
	t.Helper()
	// Relative socket names fit sun_path even beneath a long sandbox root.
	// Cleanup stops the server before Chdir restores the working directory.
	t.Chdir(t.TempDir())
	paths := fakeDaemonPaths{root: "."}
	remote := &recorder{name: daemonName}
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(token string) (principal.Principal, error) {
		if token != fakeToken {
			t.Error("daemon received a different principal token")
		}
		return principal.Principal{ID: "test-principal", Kind: principal.KindSession,
			SessionID: "test-session", Scope: "project:test"}, nil
	})
	for _, method := range []string{"inbox.list", "inbox.read"} {
		reg.Register(method, func(ctx context.Context, _ json.RawMessage) (any, error) {
			return remote.handler(method)(ctx, nil)
		})
	}
	serveUntilCleanup(t, paths.SocketPath(), func(ctx context.Context) error {
		ln, err := runtime.ListenOwnerSocket(paths.SocketPath())
		if err != nil {
			return err
		}
		defer func() {
			if cerr := ln.Close(); cerr != nil && !strings.Contains(cerr.Error(), "use of closed network connection") {
				// serveRPCListener closes the listener itself on shutdown.
				t.Errorf("close listener: %v", cerr)
			}
		}()
		return serveRPCListener(ctx, ln, rpc.NewHandler(reg), rpc.ConnContext)
	})
	return paths, remote
}
