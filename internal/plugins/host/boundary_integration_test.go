//go:build integration

// TestHostBoundary_RealPluginABI is the T3+T4 chain proof (Art.2
// real-counterpart, LANE-RULES §1): it drives a REAL
// process.ProcessRuntime.Launch against a real forked OS process
// (/bin/sh, not this repo's code — mirroring
// internal/plugins/process/runtime_extra_test.go's own
// TestProcessRuntime_RealCounterpart) that performs the cascade.hello
// handshake and then writes the exact host_http_request notification
// frame recorded in testdata/fixtures/plugin_abi_http_call.json to its
// stdout. It asserts the frame is denied at the REAL host boundary — the
// process runtime's notification-consumption loop (hostcalls.go) — not
// by calling HostBoundaryEnforcer.CheckHTTP directly.
//
// It lives behind the integration tag (not net, but a real subprocess
// spawn and a short wait) per this ticket's own checks list. This file
// is the one place in this package allowed to import
// internal/plugins/process: golangci's plugins-providers-boundary
// depguard rule excludes _test.go files (.golangci.yml), the same
// exemption internal/plugins/process/types.go documents for its own
// local-seam pattern.
package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins/process"
)

// checkerAdapter satisfies process.HostCapabilityChecker over a real
// *HostBoundaryEnforcer. It is the composition-root-shaped adapter a
// production wiring point (outside this ticket's files_scope) would
// build; this test builds its own to drive the real T3+T4 chain.
type checkerAdapter struct{ enforcer *HostBoundaryEnforcer }

func (a checkerAdapter) CheckHTTP(ctx context.Context, url string) error {
	return a.enforcer.CheckHTTP(ctx, url)
}
func (a checkerAdapter) CheckStorage(ctx context.Context, domain string) error {
	return a.enforcer.CheckStorage(ctx, domain)
}
func (a checkerAdapter) CheckSecretRef(ctx context.Context, key string) (any, error) {
	return a.enforcer.CheckSecretRef(ctx, key)
}
func (a checkerAdapter) CheckGeneric(ctx context.Context, capability string) error {
	return a.enforcer.CheckGeneric(ctx, capability)
}

// chanAuditSink delivers every AuditEvent on a channel so the test can
// block for one with a timeout instead of polling or sleeping.
type chanAuditSink struct{ events chan AuditEvent }

func (s chanAuditSink) LogHostCall(_ context.Context, event AuditEvent) { s.events <- event }

// pluginScript reads the fixture frame from the repo tree (never
// re-typed inline) and prints it after answering the handshake, so the
// fixture file is the literal wire bytes this test exercises.
func pluginScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "plugin_abi_http_call.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	frame := strings.TrimSpace(string(raw))
	return "read _line; printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocol_version\":\"1.0.0\",\"manifest_hash\":\"deadbeef\"}}'; printf '%s\\n' '" +
		frame + "'; sleep 1"
}

func TestHostBoundary_RealPluginABI(t *testing.T) {
	sink := chanAuditSink{events: make(chan AuditEvent, 8)}
	// Grants deliberately do NOT cover outside.example.net (the fixture's
	// URL): this is the denial this test exists to prove.
	enforcer, err := NewHostBoundaryEnforcer("sh-real-plugin", Grants{NetScopes: []string{"api.allowed.example"}}, nil, nil, sink)
	if err != nil {
		t.Fatalf("build enforcer: %v", err)
	}

	launch := func(checker process.HostCapabilityChecker) (*process.Handle, error) {
		rt := process.NewProcessRuntime()
		rt.Stderr = &discardWriter{}
		rt.Registrar = noopRegistrar{}
		rt.CapabilityChecker = checker
		// One crash is terminal: the script exits after its sleep, and a
		// respawn would re-run it and emit a second event into sink after
		// this subtest has already returned. RestartPolicy{} resolves to
		// process.DefaultRestartPolicy when BOTH fields are the zero
		// value (restart.go's resolved()), so MaxAttempts: 0 alone is
		// read as "unset" and silently restarts anyway; a nonzero
		// InitialBackoff is what actually makes MaxAttempts: 0 stick.
		rt.Restart = process.RestartPolicy{MaxAttempts: 0, InitialBackoff: time.Nanosecond}
		m := process.Manifest{Name: "sh-real-plugin", TrustTier: process.TrustTierTrusted, Command: "sh", Args: []string{"-c", pluginScript(t)}}
		return rt.Launch(context.Background(), m)
	}

	t.Run("wired: the real boundary denies the fixture call", func(t *testing.T) {
		if _, err := launch(checkerAdapter{enforcer: enforcer}); err != nil {
			t.Skipf("real sh subprocess unavailable, skipping real-boundary check: %v", err)
		}
		select {
		case ev := <-sink.events:
			if ev.Allowed || ev.CallType != httpCallType {
				t.Fatalf("audit event = %+v, want a denied http entry", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the real plugin's host_http_request call to be denied")
		}
	})

	t.Run("unwired: removing CapabilityChecker lets the same call through unchecked (mutation proof)", func(t *testing.T) {
		if _, err := launch(nil); err != nil {
			t.Skipf("real sh subprocess unavailable, skipping real-boundary check: %v", err)
		}
		select {
		case ev := <-sink.events:
			t.Fatalf("an unwired ProcessRuntime must never reach the enforcer, got %+v", ev)
		case <-time.After(1500 * time.Millisecond):
			// No event: the same fixture call passed through unchecked,
			// which is exactly what removing the wiring must produce.
		}
	})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// noopRegistrar accepts any egress class registration. Launch requires a
// non-nil Registrar; this test does not exercise egress itself.
type noopRegistrar struct{}

func (noopRegistrar) Register(process.EgressClass, process.EgressRegistrationConfig) error {
	return nil
}
