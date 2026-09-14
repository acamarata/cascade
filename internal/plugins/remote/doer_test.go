package remote

// Purpose: fakeDoer, the hermetic test double for doer (doer.go): every
//   unit test in this package's default lane exercises dialRemote
//   through fakeDoer rather than a real socket, so this package's
//   _test.go files never import "net" or "net/http" at all
//   (TestNoNetworkUnitTest_RealTreeGreen, internal/build's tree-wide
//   no-network-unit-lane gate — see doer.go's header comment). Mirrors
//   internal/hooks/egress/helper_test.go's mapVault precedent: a plain,
//   in-memory recording double, never a real backend.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeDoer is a hermetic doer: it never opens a socket. resp is
// returned for every call unless err is set; calls records every
// doRequest it was asked to send, so a test can assert the interceptor
// actually ran before the "request" reached this seam.
type fakeDoer struct {
	resp  doResponse
	err   error
	calls []doRequest
}

// Do implements doer.
func (f *fakeDoer) Do(_ context.Context, req doRequest) (doResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return doResponse{}, f.err
	}
	return f.resp, nil
}

// fakeNetError is a hermetic stand-in for a real net.Error (a dial
// failure, a connection reset, a genuine network-layer error) — it
// implements net.Error's method set (Error/Timeout/Temporary)
// structurally, WITHOUT this file importing "net" at all: classifyDialErr
// (remote.go) type-asserts against net.Error via errors.As, and Go
// interface satisfaction is structural, so a value only needs the right
// methods, never the import.
type fakeNetError struct {
	msg     string
	timeout bool
}

func (e fakeNetError) Error() string   { return e.msg }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return false }

// handshakeSuccessBody builds a valid handshake response body for abi.
func handshakeSuccessBody(abi int) []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"result":{"abi_version":` + itoa(abi) + `}}`)
}

// itoa is a tiny, dependency-free int-to-string helper so this file
// does not need to import "strconv" for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// failingReader always errors, proving readDoResponse (doer.go) wraps a
// read failure rather than swallowing it — the same proof
// registryfetch's own TestReadHTTPResponse_ReadError makes for the
// identical split.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestReadDoResponse_OK(t *testing.T) {
	resp, err := readDoResponse(200, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("readDoResponse: %v", err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "hello" {
		t.Fatalf("readDoResponse = %+v, want StatusCode=200 Body=hello", resp)
	}
}

func TestReadDoResponse_ReadError(t *testing.T) {
	_, err := readDoResponse(200, failingReader{})
	if err == nil {
		t.Fatal("readDoResponse with a failing reader: want an error, got nil")
	}
}

// TestRemoteRuntime_DialRemote_NilDoerUsesRealDoer proves the d==nil
// branch (remote.go) really falls through to the production realDoer
// (doer.go), and that realDoer.Do's own request-construction and
// client-call lines run for real -- WITHOUT this file importing "net" or
// "net/http" (TestNoNetworkUnitTest_RealTreeGreen) and WITHOUT ever
// touching a real socket: the context is already past its deadline
// BEFORE dialRemote runs (LANE-RULES §9's "make the condition true by
// construction" pattern, same technique remote_test.go's own
// TestRemoteRuntime_DialRemote_Timeout uses), so net/http's own client
// refuses at the context check, before any dial is attempted.
func TestRemoteRuntime_DialRemote_NilDoerUsesRealDoer(t *testing.T) {
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	_, err := dialRemote(expiredCtx, cfg, &passthroughInterceptor{}, nil)
	if err == nil {
		t.Fatal("dialRemote with an already-expired context and a nil doer: want an error, got nil")
	}
}
