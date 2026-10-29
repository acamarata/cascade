//go:build integration && !windows

// Purpose: fleet.capacity's SSE topic over a real unix socket (a peer credential is what
// separates the owner from a principal): a principal is refused 403, the owner is served.
// SPORT: cmd/cascade/daemon (ADD, P1-CAP-05).
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// capacityServer serves the composed handler on an owner socket and returns
// an HTTP client and a principal token.
func capacityServer(t *testing.T, d capDaemon) (*http.Client, string) {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	m := principal.NewMinter(clock)
	token, err := m.Mint(sessionPrincipal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d.c.Registry.SetAccessPolicy(func(tok string) (principal.Principal, error) { return m.Resolve(tok, clock.Now()) })
	dir, err := os.MkdirTemp("", "cfc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	d.c.Opts.Server.Handler = rpc.NewHandlerWithSSE(d.c.Registry, d.c.Events)
	go func() { _ = d.c.Opts.Server.Serve(ln) }()
	t.Cleanup(func() { _ = d.c.Opts.Server.Close() })
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return client.UnixDialer(ctx, socket) }}}
	t.Cleanup(hc.CloseIdleConnections)
	return hc, token
}

// subscribe opens GET /events?topic=fleet.capacity as the owner or a principal.
func subscribe(ctx context.Context, t *testing.T, hc *http.Client, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://unix/events?topic="+capacity.FleetCapacityTopic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(principal.HeaderToken, token)
	} else {
		req.Header.Set(principal.HeaderOwnerCLI, "owner-cli")
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestFleetCapacityTopicOwnerOnly drives the real unix socket: a principal subscriber is refused
// 403, the owner sees fleet.capacity_changed after a registry change.
func TestFleetCapacityTopicOwnerOnly(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	d := composeCapacity(t)
	hc, token := capacityServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), testDaemonBound)
	defer cancel()
	if resp := subscribe(ctx, t, hc, token); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("principal subscriber = %d, want 403", resp.StatusCode)
	}
	resp := subscribe(ctx, t, hc, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner subscriber = %d, want 200", resp.StatusCode)
	}
	got := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), string(capacity.FleetCapacityChangedKind)) {
				close(got)
				return
			}
		}
	}()
	// One registry change per refresh interval until the owner sees the event.
	for i := 0; ; i++ {
		seedCapProvider(t, d.parts.Registry, registry.ProviderRecord{Name: fmt.Sprintf("topic-prov-%d", i), Driver: registry.DriverOpenAICompat,
			BaseURL: "https://example.invalid/v1", Auth: registry.AuthKey, Tier: registry.TierMid})
		select {
		case <-got:
			return
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			t.Fatal("no fleet.capacity_changed reached the owner subscriber")
		}
	}
}
