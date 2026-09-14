package remote

// Purpose: unit coverage for dialRemote/Dispatch: handshake success,
//   version mismatch, connection refused, and timeout — the four paths
//   the ticket's acceptance criteria name explicitly, each a typed
//   *cascade.Error with the right Kind. Every case runs through fakeDoer
//   (doer_test.go), never a real socket: this package's _test.go files
//   must not import "net" or "net/http" at all
//   (TestNoNetworkUnitTest_RealTreeGreen — see doer.go's header comment
//   for the real violation this rewrite resolves; the real-socket
//   counterpart lives in remote_integration_test.go, `//go:build
//   integration`). Run with -race per LANE-RULES §7/AGENT-BRIEF checks.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// passthroughInterceptor is the pass-through Interceptor test double:
// every call returns content unchanged, proving dialRemote actually
// calls Intercept (recorded calls) without depending on the real
// internal/hooks/egress package this file cannot import in production
// code, but CAN in a _test.go file (see egress_test.go, which does
// exactly that against the real package).
type passthroughInterceptor struct {
	calls int
	last  []byte
}

func (p *passthroughInterceptor) Intercept(_ context.Context, content []byte) ([]byte, error) {
	p.calls++
	p.last = content
	return content, nil
}

func TestRemoteRuntime_DialRemote_HandshakeSuccess(t *testing.T) {
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: handshakeSuccessBody(HostABIVersionV1)}}
	interceptor := &passthroughInterceptor{}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	conn, err := dialRemote(context.Background(), cfg, interceptor, d)
	if err != nil {
		t.Fatalf("dialRemote: %v", err)
	}
	if conn.abiVersion != HostABIVersionV1 {
		t.Errorf("conn.abiVersion = %d, want %d", conn.abiVersion, HostABIVersionV1)
	}
	if interceptor.calls != 1 {
		t.Errorf("interceptor.calls = %d, want 1 (every handshake byte must transit Intercept)", interceptor.calls)
	}
	if len(interceptor.last) == 0 {
		t.Error("interceptor.last is empty, want the encoded handshake request body")
	}
	if len(d.calls) != 1 || d.calls[0].URL != cfg.endpoint() {
		t.Errorf("doer.calls = %v, want exactly one call to %q", d.calls, cfg.endpoint())
	}
}

func TestRemoteRuntime_DialRemote_VersionMismatch(t *testing.T) {
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: handshakeSuccessBody(HostABIVersionV1 + 1)}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote: want a version-mismatch error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindConflict, true)", kind, ok)
	}
}

func TestRemoteRuntime_DialRemote_ConnectionRefused(t *testing.T) {
	d := &fakeDoer{err: fakeNetError{msg: "dial tcp: connection refused", timeout: false}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote: want a connection-refused error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}

func TestRemoteRuntime_DialRemote_Timeout(t *testing.T) {
	// Deadline already expired BEFORE dialRemote even runs, by
	// construction (LANE-RULES §9: never race platform timer
	// granularity) -- so classifyDialErr's reqCtx.Err() check
	// deterministically sees context.DeadlineExceeded, with no sleep and
	// no flakiness. The doer error itself can be anything; it is the
	// wrapped cause, not the classification signal.
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	d := &fakeDoer{err: errors.New("request canceled")}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(expiredCtx, cfg, &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote: want a timeout error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindTimeout {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindTimeout, true)", kind, ok)
	}
}

func TestRemoteRuntime_DialRemote_GenericDoerFailure(t *testing.T) {
	// classifyDialErr's final fallback branch: an error that is neither
	// a context-deadline nor a net.Error.
	d := &fakeDoer{err: errors.New("boom")}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}

func TestRemoteRuntime_DialRemote_NilInterceptorFailsClosed(t *testing.T) {
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1}
	_, err := dialRemote(context.Background(), cfg, nil, &fakeDoer{})
	if err == nil {
		t.Fatal("dialRemote with nil interceptor: want a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}

func TestRemoteRuntime_DialRemote_InvalidConfigRefused(t *testing.T) {
	_, err := dialRemote(context.Background(), RemoteRuntimeConfig{}, &passthroughInterceptor{}, &fakeDoer{})
	if err == nil {
		t.Fatal("dialRemote with empty host: want a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindInvalidInput, true)", kind, ok)
	}
}

// TestRemoteRuntime_Dispatch_NotEnabled proves the Art.1.3 false-flag path: no dial is
// attempted at all when enableRemoteRuntime is false (mutation-provable
// below), and the returned error is ErrRemoteRuntimeNotEnabled's own
// message, not a generic refusal.
func TestRemoteRuntime_Dispatch_NotEnabled(t *testing.T) {
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1}
	err := Dispatch(context.Background(), cfg, false, "demo", nil)
	if err == nil {
		t.Fatal("Dispatch(enabled=false): want ErrRemoteRuntimeNotEnabled, got nil")
	}
	if !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("Dispatch(enabled=false) error = %q, want it to contain %q", err.Error(), "not yet available")
	}
	// MUTATION PROOF (LANE-RULES §4, prose form since the literal marker
	// string is banned in source): dialRemote requires a non-empty host
	// (validate()) and this cfg's host IS non-empty and its port (1) is
	// technically dialable, so if Dispatch's guard were removed or
	// inverted, this call would instead attempt a real dial (via the
	// production realDoer, since Dispatch never injects a fake) and
	// either hang past a real deadline or return a connection-refused
	// KindUnavailable error instead of the KindUnsupported this asserts
	// -- a materially different, observable failure.
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
}
