//go:build !windows && integration

package agents

// Purpose: the shared fixture of the owner-guard integration tests: the one spy
// guard, a real rpc.Handler on a real unix socket, and helpers that run
// testdata/reapchild as a dialing peer.
// Constraints: see peer_guard_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	ownerVerb  = "test.agentpeer.owner"
	scopedVerb = "test.agentpeer.scoped"
	refusal    = "agent processes may not act as the owner"
)

var _ = rpc.DeclareAccess(ownerVerb, rpc.AccessOwnerOnly)
var _ = rpc.DeclareAccess(scopedVerb, rpc.AccessScoped)

// spy is the one installed guard's state: the live list the real
// AgentPeerGuard reads, and every peer pid it was asked about.
var spy struct {
	once sync.Once
	mu   sync.Mutex
	live []LiveSpawn
	seen []int
}

// useLive installs the spy guard (once) and sets the live list and the
// recorded pids for one test; both are cleared at cleanup.
func useLive(t *testing.T, spawns ...LiveSpawn) {
	t.Helper()
	spy.once.Do(func() {
		inner := AgentPeerGuard(func() []LiveSpawn {
			spy.mu.Lock()
			defer spy.mu.Unlock()
			return append([]LiveSpawn(nil), spy.live...)
		})
		rpc.SetOwnerPeerGuard(func(ctx context.Context, pid int) error {
			spy.mu.Lock()
			spy.seen = append(spy.seen, pid)
			spy.mu.Unlock()
			return inner(ctx, pid)
		})
	})
	set := func(s []LiveSpawn) {
		spy.mu.Lock()
		spy.live, spy.seen = s, nil
		spy.mu.Unlock()
	}
	set(spawns)
	t.Cleanup(func() { set(nil) })
}

// addLive appends one spawn to the live list.
func addLive(sp LiveSpawn) {
	spy.mu.Lock()
	spy.live = append(spy.live, sp)
	spy.mu.Unlock()
}

// seenPIDs returns the pids the guard was asked about.
func seenPIDs() []int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return append([]int(nil), spy.seen...)
}

// ownerServer is a real handler on a real unix socket.
type ownerServer struct {
	sock     string
	token    string
	accepted chan struct{} // one value per connection whose ConnContext has run
	mu       sync.Mutex
	served   map[string]int
}

func (s *ownerServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[method]
}

// startOwnerServer serves ownerVerb (owner only) and scopedVerb (principal
// only) over a unix socket in a short temp dir; the handlers count their calls.
func startOwnerServer(t *testing.T) *ownerServer { return startGatedOwnerServer(t, nil) }

// gatedListener holds every Accept until gate is closed, so a client can finish
// connect() and even write while the daemon has not yet accepted.
type gatedListener struct {
	net.Listener
	gate <-chan struct{}
}

func (g gatedListener) Accept() (net.Conn, error) {
	<-g.gate
	return g.Listener.Accept()
}

// startGatedOwnerServer is startOwnerServer whose Accept waits for gate (nil =
// no wait). Its ConnContext is the daemon's rpc.GuardedConnContext(rpc.ConnContext), then it announces the
// connection on ownerServer.accepted.
func startGatedOwnerServer(t *testing.T, gate <-chan struct{}) *ownerServer {
	t.Helper()
	dir, err := os.MkdirTemp("", "agpeer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	minter := principal.NewMinter(clock)
	token, err := minter.Mint(principal.Principal{ID: "agent-test", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := &ownerServer{sock: filepath.Join(dir, "d.sock"), token: token, accepted: make(chan struct{}, 16), served: map[string]int{}}
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, clock.Now()) })
	for _, method := range []string{ownerVerb, scopedVerb} {
		reg.Register(method, func(context.Context, json.RawMessage) (any, error) {
			s.mu.Lock()
			s.served[method]++
			s.mu.Unlock()
			return "served", nil
		})
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatal(err)
	}
	served := ln
	if gate != nil {
		served = gatedListener{Listener: ln, gate: gate}
	}
	srv := &http.Server{Handler: rpc.NewHandler(reg), ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		ctx = rpc.GuardedConnContext(rpc.ConnContext)(ctx, c)
		s.accepted <- struct{}{}
		return ctx
	}}
	go func() { _ = srv.Serve(served) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

// reply is one HTTP answer.
type reply struct {
	status int
	body   string
}

// errorKind and errorMessage read the JSON-RPC error of a reply.
func (r reply) errorMessage() (message, kind string) {
	var env struct {
		Error struct {
			Message string            `json:"message"`
			Data    map[string]string `json:"data"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(r.body), &env)
	return env.Error.Message, env.Error.Data["kind"]
}

// wantRefused fails unless r is the guard's HTTP 403 KindPermissionDenied.
func wantRefused(t *testing.T, name string, r reply) {
	t.Helper()
	msg, kind := r.errorMessage()
	if r.status != 403 || msg != refusal || kind != cascade.KindPermissionDenied.String() {
		t.Errorf("%s: status %d message %q kind %q (body %q), want 403 %q %q", name, r.status, msg, kind, r.body, refusal, cascade.KindPermissionDenied.String())
	}
}

// selfDial sends one call from the test process itself.
func selfDial(t *testing.T, sock, method, header, value string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://unix/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(header, value)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: resp.StatusCode, body: string(b)}
}

// dialChild runs reapchild's sub ("dial" or "setsid-dial") in its own process
// group with marker in its environment (none when empty), publishes its
// LiveSpawn (register builds it from the pid) before releasing it, and returns
// what it recorded.
func dialChild(t *testing.T, bin, sub, sock, mode, method, marker string, register func(pid int) LiveSpawn, publish func(LiveSpawn), attr *syscall.SysProcAttr) reply {
	t.Helper()
	dir := t.TempDir()
	out, gofile := filepath.Join(dir, "out"), filepath.Join(dir, "go")
	cmd := exec.Command(bin, sub, sock, out, mode, method, gofile)
	cmd.Env = childEnv(t, marker)
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reapchild: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if register != nil {
		publish(register(cmd.Process.Pid))
	}
	if err := os.WriteFile(gofile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reapchild %s: %v", sub, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reapchild recorded nothing: %v", err)
	}
	status, body, _ := strings.Cut(string(b), "\n")
	code, err := strconv.Atoi(status)
	if err != nil {
		t.Fatalf("reapchild status %q: %v", status, err)
	}
	return reply{status: code, body: body}
}

// inOwnGroup and inOwnSession are how a dialing child is placed: its own process
// group (the usual shape of a spawn), or its own session (an owner's terminal).
var (
	inOwnGroup   = &syscall.SysProcAttr{Setpgid: true}
	inOwnSession = &syscall.SysProcAttr{Setsid: true}
)

// leaderLive registers the whole child (marker, group, leader pid).
func leaderLive(marker string) func(int) LiveSpawn {
	return func(pid int) LiveSpawn { return LiveSpawn{Marker: marker, PGID: pid, PID: pid} }
}
