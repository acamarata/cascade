//go:build !windows && integration

package client

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// shortSocketPath returns a socket path in a short temp dir so long test
// names and temp prefixes never overflow sockaddr_un.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cmf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func modelHeaderClient(t *testing.T, headers chan<- http.Header) *Client {
	t.Helper()
	socket := shortSocketPath(t, "h.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": true, "protocol_version": "1.0.0", "server_version": "1.0.0"}); err != nil {
			t.Error(err)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return New(socket, UnixDialer, time.Second)
}

func checkModelHeaders(t *testing.T, h http.Header, marker string, token bool) {
	t.Helper()
	if token {
		if h.Get(principal.HeaderToken) != "test-principal" || len(h.Values(principal.HeaderOwnerCLI)) != 0 {
			t.Fatal("token must be exclusive")
		}
	} else if h.Get(principal.HeaderOwnerCLI) != marker || len(h.Values(principal.HeaderToken)) != 0 || strings.Contains(h.Get(principal.HeaderOwnerCLI), ",") {
		t.Fatal("wrong client marker")
	}
}

func TestModelFacingClientHeaders(t *testing.T) {
	headers := make(chan http.Header, 1)
	cli := modelHeaderClient(t, headers)
	model := cli.ModelFacing()
	if model == cli || model.httpClient == cli.httpClient {
		t.Fatal("ModelFacing must copy its receiver and HTTP client")
	}
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	for _, token := range []bool{false, true} {
		if token {
			t.Setenv(principal.EnvToken, "test-principal")
		}
		for _, owner := range []bool{false, true} {
			ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: owner})
			for _, item := range []struct {
				c      *Client
				marker string
			}{{cli, "owner-cli"}, {model, "owner-model"}} {
				if err := item.c.Do(ctx, "status.get", nil, nil); err != nil {
					t.Fatal(err)
				}
				checkModelHeaders(t, <-headers, item.marker, token)
			}
		}
	}
}

var _ = rpc.DeclareAccess("test.client.ownerview", rpc.AccessOwnerView)

type ownerViewProbe struct {
	socket string
	calls  int
	caller principal.Caller
	found  bool
}

// startOwnerViewDaemon serves the real rpc.Handler with access checks on a
// unix socket in a short temp dir; every token is refused as unknown.
func startOwnerViewDaemon(t *testing.T) *ownerViewProbe {
	t.Helper()
	probe := &ownerViewProbe{socket: shortSocketPath(t, "d.sock")}
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "caller principal not recognized")
	})
	reg.Register("test.client.ownerview", func(ctx context.Context, _ json.RawMessage) (any, error) {
		probe.calls++
		probe.caller, probe.found = principal.CallerFrom(ctx)
		return true, nil
	})
	ln, err := net.Listen("unix", probe.socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rpc.NewHandler(reg), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return probe
}

func unsetPrincipalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCLIKeepsOwnerView(t *testing.T) {
	unsetPrincipalEnv(t)
	probe := startOwnerViewDaemon(t)
	cli := New(probe.socket, UnixDialer, time.Second)
	if err := cli.Do(context.Background(), "test.client.ownerview", nil, nil); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || !probe.found || probe.caller.Principal != nil || !probe.caller.OwnerCLI {
		t.Fatal(probe.calls, probe.found, probe.caller)
	}
}

func TestOwnerModelMarkerResolvesOwnerViewWithoutLocalOnly(t *testing.T) {
	unsetPrincipalEnv(t)
	probe := startOwnerViewDaemon(t)
	model := New(probe.socket, UnixDialer, time.Second).ModelFacing()
	for i, owner := range []bool{false, true} {
		ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: owner})
		if err := model.Do(ctx, "test.client.ownerview", nil, nil); err != nil {
			t.Fatal(err)
		}
		if probe.calls != i+1 || !probe.found || probe.caller.Principal != nil || probe.caller.OwnerCLI {
			t.Fatal(probe.calls, probe.found, probe.caller)
		}
	}
}

func TestBothHeadersRefused(t *testing.T) {
	probe := startOwnerViewDaemon(t)
	raw := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", probe.socket)
	}}}
	req, err := http.NewRequest(http.MethodPost, "http://unix/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test.client.ownerview"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(principal.HeaderOwnerCLI, "owner-model")
	req.Header.Set(principal.HeaderToken, "test-principal")
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusBadRequest || probe.calls != 0 ||
		!strings.Contains(string(body), `"message":"conflicting caller headers"`) || !strings.Contains(string(body), `"kind":"invalid-input"`) {
		t.Fatal(resp.StatusCode, probe.calls, string(body), err)
	}
	// With the process token set, ModelFacing sends the token alone: the
	// daemon judges the token and never sees two caller headers.
	t.Setenv(principal.EnvToken, "test-principal")
	err = New(probe.socket, UnixDialer, time.Second).ModelFacing().Do(context.Background(), "test.client.ownerview", nil, nil)
	kind, _ := cascade.KindOf(err)
	if kind != cascade.KindPermissionDenied || !strings.Contains(err.Error(), "caller principal not recognized") || probe.calls != 0 {
		t.Fatal(err, probe.calls)
	}
}
