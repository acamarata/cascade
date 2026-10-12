//go:build !windows && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = rpc.DeclareTopicAccess("test.owner", rpc.AccessOwnerOnly)
var _ = rpc.DeclareTopicAccess("test.visible", rpc.AccessOwnerView)

// startPrincipalServer serves the composed handler over an owner-authenticated Unix socket.
func startPrincipalServer(t *testing.T, c composedForTest) (string, *http.Client, string) {
	t.Helper()
	t.Setenv("USERPROFILE", t.TempDir())
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	m := principal.NewMinter(clock)
	token, err := m.Mint(principal.Principal{ID: "test", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Registry.SetAccessPolicy(func(token string) (principal.Principal, error) { return m.Resolve(token, clock.Now()) })
	dir, err := os.MkdirTemp("", "cpt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	c.Opts.Server.Handler = rpc.NewHandlerWithSSE(c.Registry, c.Events)
	go func() { _ = c.Opts.Server.Serve(ln) }()
	t.Cleanup(func() { _ = c.Opts.Server.Close() })
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return client.UnixDialer(ctx, socket) }}}
	t.Cleanup(httpClient.CloseIdleConnections)
	return socket, httpClient, token
}

func principalHTTP(t *testing.T, c *http.Client, path, token, marker string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://unix"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(principal.HeaderToken, token)
	}
	if marker != "" {
		req.Header.Set(principal.HeaderOwnerCLI, marker)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestSSETopicRefusesAnonymous(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	var calls atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) })
	c.Events.Default = h
	c.Events.WithTopic("test.owner", h)
	c.Events.WithTopic("fleet.sessions", visibleStub(h))
	_, httpClient, token := startPrincipalServer(t, c)
	for _, path := range []string{"/events", "/events?topic=test.owner", "/events?topic=fleet.sessions"} {
		if code, body := principalHTTP(t, httpClient, path, "", ""); code != 403 {
			t.Fatal(code, body)
		}
		if path != "/events?topic=fleet.sessions" {
			if code, body := principalHTTP(t, httpClient, path, token, ""); code != 403 {
				t.Fatal(code, body)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("refused stream reached handler", calls.Load())
	}
	for _, path := range []string{"/events", "/events?topic=test.owner", "/events?topic=fleet.sessions"} {
		if code, body := principalHTTP(t, httpClient, path, "", "owner-cli"); code != 204 {
			t.Fatal(code, body)
		}
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}

func TestAllTopicsMarked(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	if len(c.Events.Topics) == 0 || c.Events.Default == nil {
		t.Fatal("empty composed topics")
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	paths := []string{"/events"}
	c.Events.Default = h
	for topic, real := range c.Events.Topics {
		paths = append(paths, "/events?topic="+topic)
		c.Events.Topics[topic] = h
		if _, consumes := real.(rpc.EventVisibleConsumer); consumes {
			c.Events.Topics[topic] = visibleStub(h)
		}
	}
	c.Events.WithTopic("test.unmarked", h)
	c.Events.WithTopic("test.visible", h)
	_, httpClient, _ := startPrincipalServer(t, c)
	for _, path := range paths {
		if code, body := principalHTTP(t, httpClient, path, "", "owner-cli"); code != 204 {
			t.Fatalf("unmarked composed topic %s: %d %s", path, code, body)
		}
	}
	for _, topic := range []string{"test.unmarked", "test.visible"} {
		if code, body := principalHTTP(t, httpClient, "/events?topic="+topic, "", "owner-cli"); code != 403 {
			t.Fatalf("unsafe topic %s served: %d %s", topic, code, body)
		}
	}
}

// visibleStub stands in for an owner-view topic handler, which must declare
// that it applies the event predicate.
type visibleStub http.HandlerFunc

func (f visibleStub) ServeHTTP(w http.ResponseWriter, r *http.Request) { f(w, r) }
func (visibleStub) ConsumesEventVisible()                              {}

type principalVisibleHandler struct{ denied atomic.Bool }

func (*principalVisibleHandler) ConsumesEventVisible() {}
func (h *principalVisibleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pred, ok := rpc.EventVisibleFrom(r.Context())
	h.denied.Store(ok && pred != nil && !pred("session", "parent") && !pred("", ""))
	w.WriteHeader(204)
}

func TestPrincipalTopicAlwaysFiltered(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	h := &principalVisibleHandler{}
	c.Events.WithTopic("test.visible", h)
	_, httpClient, token := startPrincipalServer(t, c)
	if code, body := principalHTTP(t, httpClient, "/events?topic=test.visible", token, ""); code != 204 {
		t.Fatal(code, body)
	}
	if !h.denied.Load() {
		t.Fatal("principal received no deny-all predicate")
	}
}

func TestOwnerCLIKeepsOwnerView(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	// The vault backend is optional in this fixture; its access rule must still deny first.
	c.Registry.Register("vault.get", func(context.Context, json.RawMessage) (any, error) {
		t.Error("principal reached vault backend")
		return nil, nil
	})
	socket, _, token := startPrincipalServer(t, c)
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	cli := client.New(socket, client.UnixDialer, 5*time.Second)
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": "private fixture"}}, "privacy_mode": "local-only"}
	if err := cli.Do(context.Background(), "chat.append_turn", params, &created); err != nil || created.ThreadID == "" {
		t.Fatal(created, err)
	}
	var owner json.RawMessage
	if err := cli.Do(context.Background(), "chat.list_threads", nil, &owner); err != nil || !bytes.Contains(owner, []byte(created.ThreadID)) {
		t.Fatal(string(owner), err)
	}
	t.Setenv(principal.EnvToken, token)
	var scoped json.RawMessage
	err := cli.Do(context.Background(), "chat.list_threads", nil, &scoped)
	if err != nil || bytes.Contains(scoped, []byte(created.ThreadID)) {
		t.Fatal("owner chat thread leaked to a principal", string(scoped), err)
	}
	err = cli.Do(context.Background(), "vault.get", nil, nil)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindPermissionDenied {
		t.Fatal(err)
	}
}

func TestInstalledHookpackCommandsStillServed(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	socket, httpClient, _ := startPrincipalServer(t, c)
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	deps := productionFleetSessionsDeps()
	deps.Paths = fakeDaemonPaths{root: filepath.Dir(socket)}
	pack := hookpacks.SessionsPack()
	if len(pack.Descriptors) == 0 {
		t.Fatal("no installed hook commands")
	}
	for _, d := range pack.Descriptors {
		words := strings.Fields(d.CommandTemplate)
		if len(words) != 5 || strings.Join(words[:4], " ") != hookpacks.SessionsHookCommand {
			t.Fatal("unrecognized installed command", d.CommandTemplate)
		}
		input := loadHookFixture(t, strings.ToLower(string(d.EventType)))
		result := runHookEvent(deps, input, words[4])
		if result.err != nil || result.stderr != "" {
			t.Fatal(d.EventType, result)
		}
	}
	records := listLiveSessions(t, deps)
	if len(records) == 0 {
		t.Fatal("commands wrote no session state")
	}
	assertAnonymousHook(t, c, httpClient, deps)
}

func assertAnonymousHook(t *testing.T, c composedForTest, httpClient *http.Client, deps fleetSessionsDeps) {
	t.Helper()
	payload := hookpacks.HookPayload{SessionID: "anonymous-hook", Harness: sessionHookHarness, EventType: hookpacks.EventSessionStart, TimestampMs: c.Opts.Clock.Now().UnixMilli()}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": hookpacks.MethodHookEvent, "params": payload})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpClient.Post("http://unix/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Error *rpc.ErrorObject `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || env.Error != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, env.Error, err)
	}
	for _, record := range listLiveSessions(t, deps) {
		if record.SessionID == payload.SessionID {
			return
		}
	}
	t.Fatal("anonymous ingest did not store its session")
}

func TestDaemonVerbsAllMarked(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	methods := c.Registry.Methods()
	if len(methods) == 0 {
		t.Fatal("empty daemon registry")
	}
	ctx := principal.WithCaller(context.Background(), principal.Caller{})
	// A replacement handler prevents side effects while retaining the real registry policy.
	for _, name := range methods {
		c.Registry.Register(name, func(context.Context, json.RawMessage) (any, error) { return true, nil })
		_, err := c.Registry.Dispatch(ctx, &rpc.Request{Method: name})
		if err != nil && err.Message == "verb has no access marking" {
			t.Errorf("unmarked: %s", name)
		}
	}
	c.Registry.Register("test.unmarked", func(context.Context, json.RawMessage) (any, error) { t.Error("unmarked handler ran"); return nil, nil })
	_, err := c.Registry.Dispatch(ctx, &rpc.Request{Method: "test.unmarked"})
	if err == nil || err.Message != "verb has no access marking" {
		t.Fatal(err)
	}
}
