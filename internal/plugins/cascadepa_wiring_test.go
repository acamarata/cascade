// Purpose: unit coverage for cascadepa_wiring.go's adapter, isolated from
//
//	the real home directory (Art.7.1: a fake pathResolver stands in for
//	runtime.NewDefaultPathProvider) and, per internal/build's
//	no-network-unit-lane gate (Art.7.2), never naming the "net" package
//	itself -- the real client.UnixDialer (production code, which does
//	import "net") is used against a socket path nothing listens on,
//	exactly as internal/client/transport_test.go's own
//	TestStatus_TransportFailurePropagates does for the identical reason.
//
// SPORT: internal/plugins:cascadepa-wiring (TEST) -- FIX-cascade-chat-client-wiring.
package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// fakePathProvider satisfies runtime.PathProvider with fixed strings, so
// tests never touch the real home directory.
type fakePathProvider struct{ socket string }

func (f fakePathProvider) Root() string                       { return "/fake" }
func (f fakePathProvider) ConfigPath() string                 { return "/fake/config.toml" }
func (f fakePathProvider) SocketPath() string                 { return f.socket }
func (f fakePathProvider) DataDir() string                    { return "/fake/data" }
func (f fakePathProvider) LogDir() string                     { return "/fake/logs" }
func (f fakePathProvider) StorageRoot(runtime.Profile) string { return "/fake/data/storage" }

// missingSocketPath returns a path under t.TempDir() that nothing binds,
// so client.UnixDialer's real dial genuinely fails without a live daemon.
func missingSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nothing-here.sock")
}

func TestCascadePAClient_OneShot_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("OneShot: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("OneShot: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePAClient_OneShot_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("OneShot: err = nil, want a transport error")
	}
	// This is the load-bearing assertion: a real internal/client.Client
	// transport failure, never unconfiguredClient's canned message and
	// never a fabricated success.
	if got := err.Error(); !containsAll(got, "chat.append_turn", "daemon not running or unreachable") {
		t.Errorf("OneShot: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePAClient_Stream_ClosesTokensBeforeError(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	tokens, errs := c.Stream(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})

	var sawToken bool
	for range tokens {
		sawToken = true
	}
	if sawToken {
		t.Error("Stream: sent a token, want zero tokens (no daemon component streams a reply yet)")
	}
	err := <-errs
	if err == nil {
		t.Fatal("Stream: err = nil, want a transport error")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
