package remote

// Purpose: additional dialRemote/Dispatch branch coverage split out of
//   remote_test.go, which hit the repo's 300-line cap (LANE-RULES §8;
//   same precedent P1-E15-W4-S32-T2's journal recorded for its own
//   out-of-files_scope split files) — the Art.4 SECURITY floor for this
//   package (>=90% statements, R-21.279) needs the branches this file
//   covers: an out-of-range port, an Interceptor that itself refuses, a
//   malformed response body reaching dialRemote's own decode call site
//   (not just decodeHandshakeResponse in isolation), the zero-Timeout
//   default, and Dispatch's two enabled-path branches (defers after a
//   successful handshake; propagates a failed one). Every case runs
//   through fakeDoer (doer_test.go), never a real socket — see
//   remote_test.go's header comment for the gate this satisfies.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRemoteRuntime_DialRemote_InvalidPortRefused exercises validate's
// OTHER required-field branch (a non-empty host but an out-of-range
// port), distinct from remote_test.go's empty-host case.
func TestRemoteRuntime_DialRemote_InvalidPortRefused(t *testing.T) {
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 99999}
	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, &fakeDoer{})
	if err == nil {
		t.Fatal("dialRemote with an out-of-range port: want a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindInvalidInput, true)", kind, ok)
	}
}

// erroringInterceptor always refuses, proving dialRemote propagates an
// Interceptor's own error rather than dialing anyway.
type erroringInterceptor struct{}

func (erroringInterceptor) Intercept(context.Context, []byte) ([]byte, error) {
	return nil, cascade.New(cascade.KindPolicyDenied, "test: interceptor refuses everything")
}

func TestRemoteRuntime_DialRemote_InterceptorErrorPropagates(t *testing.T) {
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1}
	d := &fakeDoer{}
	_, err := dialRemote(context.Background(), cfg, erroringInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote with a refusing interceptor: want its error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindPolicyDenied, true) (the interceptor's own kind)", kind, ok)
	}
	if len(d.calls) != 0 {
		t.Errorf("doer.calls = %v, want zero calls (a refused interceptor must never reach the wire)", d.calls)
	}
}

// TestRemoteRuntime_DialRemote_MalformedResponseBody proves dialRemote's
// own decodeHandshakeResponse call site (not just the function in
// isolation) returns a typed error for a response body that is not
// valid JSON.
func TestRemoteRuntime_DialRemote_MalformedResponseBody(t *testing.T) {
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: []byte("not json")}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dialRemote with a malformed response body: want a decode error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindInvalidInput, true)", kind, ok)
	}
}

// TestRemoteRuntime_DialRemote_DefaultTimeoutUsed proves the
// Timeout==0 branch of RemoteRuntimeConfig.timeout() (DefaultHandshakeTimeout)
// still reaches a successful handshake.
func TestRemoteRuntime_DialRemote_DefaultTimeoutUsed(t *testing.T) {
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: handshakeSuccessBody(HostABIVersionV1)}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1} // Timeout left zero

	if _, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, d); err != nil {
		t.Fatalf("dialRemote with default timeout: %v", err)
	}
}

// TestRemoteRuntime_Dispatch_EnabledDefersAfterHandshake proves the
// flag=true path: the handshake really runs (the fake doer recorded the
// call) and the call still refuses, with ErrRemoteRuntimeDeferred rather
// than success.
func TestRemoteRuntime_Dispatch_EnabledDefersAfterHandshake(t *testing.T) {
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: handshakeSuccessBody(HostABIVersionV1)}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	err := dispatchWithDoer(context.Background(), cfg, true, "demo", &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dispatchWithDoer(enabled=true): want ErrRemoteRuntimeDeferred, got nil")
	}
	if len(d.calls) != 1 {
		t.Errorf("doer.calls = %v, want exactly one real handshake attempt", d.calls)
	}
	if !strings.Contains(err.Error(), "deferred to P2") {
		t.Errorf("dispatchWithDoer(enabled=true) error = %q, want it to contain %q", err.Error(), "deferred to P2")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
}

// TestRemoteRuntime_Dispatch_EnabledHandshakeFails proves Dispatch's
// OTHER enabled-path branch: when the handshake itself fails, Dispatch
// returns THAT typed error, never ErrRemoteRuntimeDeferred (which would
// misreport a failed handshake as a successful, merely-deferred one).
func TestRemoteRuntime_Dispatch_EnabledHandshakeFails(t *testing.T) {
	d := &fakeDoer{err: fakeNetError{msg: "connection refused", timeout: false}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	err := dispatchWithDoer(context.Background(), cfg, true, "demo", &passthroughInterceptor{}, d)
	if err == nil {
		t.Fatal("dispatchWithDoer(enabled=true) over a refused connection: want the dial error, got nil")
	}
	if strings.Contains(err.Error(), "deferred to P2") {
		t.Fatal("dispatchWithDoer(enabled=true) over a refused connection reported ErrRemoteRuntimeDeferred instead of the real dial failure")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}
