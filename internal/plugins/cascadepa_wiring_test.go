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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
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

// TestCascadePAClient_Resume_TransportUnreachable mirrors
// TestCascadePAClient_OneShot_TransportUnreachable for the V/S-47.T2
// Resume method: a real internal/client.Client transport failure over a
// socket nothing listens on, never a fabricated success.
func TestCascadePAClient_Resume_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.Resume(context.Background(), pacmd.ResumeRequest{TopicID: "t1"})
	if err == nil {
		t.Fatal("Resume: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("Resume: err = %q, want internal/client's own classified transport error", got)
	}
}

// TestCascadePAClient_Resume_PathResolutionFailure mirrors OneShot's own
// identical test.
func TestCascadePAClient_Resume_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.Resume(context.Background(), pacmd.ResumeRequest{TopicID: "t1"})
	if !errors.Is(err, wantErr) {
		t.Errorf("Resume: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestCascadePAClient_CreateThread_TransportUnreachable mirrors
// TestCascadePAClient_Resume_TransportUnreachable for CreateThread: a
// real internal/client.Client transport failure over a socket nothing
// listens on.
func TestCascadePAClient_CreateThread_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.CreateThread(context.Background(), "local-only")
	if err == nil {
		t.Fatal("CreateThread: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("CreateThread: err = %q, want internal/client's own classified transport error", got)
	}
}

// TestCascadePAClient_CreateThread_PathResolutionFailure mirrors OneShot's
// own identical test.
func TestCascadePAClient_CreateThread_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.CreateThread(context.Background(), "local-only")
	if !errors.Is(err, wantErr) {
		t.Errorf("CreateThread: err = %v, want it to wrap %v", err, wantErr)
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

const fakeProjectDir = "/fake/workspace/project-a"

// cwdClient is a cascadePAClient over a recording doer with an injected getwd.
func cwdClient(getwd func() (string, error)) (*cascadePAClient, *fakeRPCDoer) {
	doer := &fakeRPCDoer{}
	return &cascadePAClient{doer: doer, getwd: getwd}, doer
}

func fixedWd(dir string) func() (string, error) { return func() (string, error) { return dir, nil } }

// wireKeys marshals params and returns its top-level JSON keys.
func wireKeys(t *testing.T, params any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

// TestCascadePAClient_SendsCwdOnAppend: every chat.append_turn from
// `cascade chat`, creating or continuing, carries the process cwd.
func TestCascadePAClient_SendsCwdOnAppend(t *testing.T) {
	for _, thread := range []string{"", "thread-existing"} {
		c, doer := cwdClient(fixedWd(fakeProjectDir))
		_, _ = c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi", Thread: thread})
		got, ok := doer.gotParams.(appendTurnParams)
		if doer.calledMethod != conversation.MethodAppendTurn || !ok || got.Cwd != fakeProjectDir {
			t.Fatalf("thread %q: %s sent %+v, want cwd %q", thread, doer.calledMethod, doer.gotParams, fakeProjectDir)
		}
	}
	if c := newCascadePAClient(nil, time.Second, nil); c.getwd == nil {
		t.Fatal("the production constructor wires no getwd, so nothing would ever send a cwd")
	} else if want, _ := os.Getwd(); func() string { d, _ := c.getwd(); return d }() != want {
		t.Fatal("the production getwd is not os.Getwd")
	}
}

// TestCascadePAClient_SendsCwdOnCreateThread: chat.create_thread carries it too.
func TestCascadePAClient_SendsCwdOnCreateThread(t *testing.T) {
	c, doer := cwdClient(fixedWd(fakeProjectDir))
	_, _ = c.CreateThread(context.Background(), "internal")
	got, ok := doer.gotParams.(createThreadParams)
	if doer.calledMethod != conversation.MethodCreateThread || !ok || got.Cwd != fakeProjectDir || got.PrivacyMode != "internal" {
		t.Fatalf("%s sent %+v, want privacy internal and cwd %q", doer.calledMethod, doer.gotParams, fakeProjectDir)
	}
}

// TestCascadePAClient_GetwdErrorSendsEmptyCwd: a getwd failure (or no getwd at
// all) sends no cwd, never a guess: the thread is created unscoped.
func TestCascadePAClient_GetwdErrorSendsEmptyCwd(t *testing.T) {
	failing := func() (string, error) { return "/must/not/be/used", errors.New("fake getwd failure") }
	for name, getwd := range map[string]func() (string, error){"error": failing, "nil": nil} {
		c, doer := cwdClient(getwd)
		_, _ = c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
		if _, has := wireKeys(t, doer.gotParams)["cwd"]; has {
			t.Fatalf("%s: append sent a cwd after getwd failed: %+v", name, doer.gotParams)
		}
		_, _ = c.CreateThread(context.Background(), "internal")
		if _, has := wireKeys(t, doer.gotParams)["cwd"]; has {
			t.Fatalf("%s: create_thread sent a cwd after getwd failed: %+v", name, doer.gotParams)
		}
	}
}

// TestBridgeChatService_NeverSendsCwd: the bridge acts for a remote chat, not
// the daemon host's shell, so neither of its calls carries a cwd.
func TestBridgeChatService_NeverSendsCwd(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		if res, ok := out.(*createThreadResultWire); ok {
			res.ThreadID = "t1"
		}
	}}
	c := bridgeChatService{doer: doer}
	if _, err := c.AppendTurn(context.Background(), "t1", "user", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, has := wireKeys(t, doer.gotParams)["cwd"]; has || doer.calledMethod != conversation.MethodAppendTurn {
		t.Fatalf("bridge %s sent %+v, want no cwd", doer.calledMethod, doer.gotParams)
	}
	if _, err := c.CreateThread(context.Background(), "internal"); err != nil {
		t.Fatal(err)
	}
	if _, has := wireKeys(t, doer.gotParams)["cwd"]; has || doer.calledMethod != conversation.MethodCreateThread {
		t.Fatalf("bridge %s sent %+v, want no cwd", doer.calledMethod, doer.gotParams)
	}
}
