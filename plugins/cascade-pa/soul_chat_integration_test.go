//go:build !windows && integration

// Purpose: the Art.2 real-counterpart proof for route (c): a live daemon
//
//	serving the genuine internal/memory.SoulHandler over a real unix
//	socket, a real internal/client transport, and HandleSoulChatCommand
//	dispatching through a SoulChatClient adapter built on that transport —
//	not the stub store the unit suite uses.
//
// Constraints: build-tagged "integration" (imports net/net-http, which the
//
//	no-network unit lane forbids); _test.go files are exempt from the
//	plugins-providers-boundary depguard rule (.golangci.yml), so this file
//	may import internal/** to stand up the real daemon side.
//
// SPORT: plugins/cascade-pa:soul-chat (ADD) — P1-E22-W5-S47-T3.
package cascadepa

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// realSoulChatClient adapts internal/client's transport to SoulChatClient,
// duplicating the wire shapes rather than importing internal/memory's
// (unexported RPC decode targets), matching
// internal/plugins/cascadepa_wiring.go's documented practice.
type realSoulChatClient struct{ rpc *client.Client }

type wireSoulShowResult struct {
	Body     string `json:"body"`
	Schema   string `json:"schema"`
	Version  int    `json:"version"`
	Diverged bool   `json:"diverged"`
}

type wireSoulEditParams struct {
	Body   string `json:"body"`
	Schema string `json:"schema,omitempty"`
}

type wireSoulEditResult struct {
	Version int `json:"version"`
}

func (c *realSoulChatClient) Show(ctx context.Context) (SoulChatView, error) {
	var res wireSoulShowResult
	if err := c.rpc.Do(ctx, memory.MethodSoulShow, struct{}{}, &res); err != nil {
		return SoulChatView{}, err
	}
	return SoulChatView{Body: res.Body, Schema: res.Schema, Version: res.Version, Diverged: res.Diverged}, nil
}

func (c *realSoulChatClient) Edit(ctx context.Context, doc SoulChatDocument) (SoulChatEditResult, error) {
	var res wireSoulEditResult
	params := wireSoulEditParams{Body: doc.Body, Schema: doc.Schema}
	if err := c.rpc.Do(ctx, memory.MethodSoulEdit, params, &res); err != nil {
		return SoulChatEditResult{}, err
	}
	return SoulChatEditResult{Version: res.Version}, nil
}

// startSoulDaemon serves the real memory.soul.* namespace over a real unix
// socket, matching cmd/cascade/memory_integration_test.go's
// startMemoryDaemon pattern for the sibling memory.* namespace.
func startSoulDaemon(t *testing.T) *client.Client {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "soulchat")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	registry := rpc.NewRegistry()
	store := memory.NewFileSoulStore(t.TempDir(), runtime.SystemClock{}, nil)
	memory.NewSoulHandler(store).Register(registry)

	sockPath := filepath.Join(sockDir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler:           rpc.NewHandler(registry),
		ConnContext:       rpc.ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return client.New(sockPath, client.UnixDialer, 5*time.Second)
}

// TestSoulChatEditRPC is the contract's required integration proof: a
// route-c edit lands in the real SOUL store over the real
// memory.soul.edit RPC, and the returned version stamp round-trips.
func TestSoulChatEditRPC(t *testing.T) {
	rpcClient := startSoulDaemon(t)
	withSoulChatClient(t, &realSoulChatClient{rpc: rpcClient})

	ctx := context.Background()
	reply, matched, err := HandleSoulChatCommand(ctx, "/soul edit body I am Ada, and I like precision.")
	if err != nil || !matched {
		t.Fatalf("HandleSoulChatCommand: matched=%v err=%v", matched, err)
	}
	if !strings.Contains(reply, "version 1") {
		t.Fatalf("reply = %q, want it to name version 1 (first write)", reply)
	}

	// A second edit against the SAME live store proves the version is
	// really persisted server-side, not merely echoed by a fake.
	reply2, matched2, err2 := HandleSoulChatCommand(ctx, "/soul edit schema mine/v3")
	if err2 != nil || !matched2 {
		t.Fatalf("HandleSoulChatCommand (2nd): matched=%v err=%v", matched2, err2)
	}
	if !strings.Contains(reply2, "version 2") {
		t.Fatalf("2nd reply = %q, want it to name version 2", reply2)
	}

	view, err := (&realSoulChatClient{rpc: rpcClient}).Show(ctx)
	if err != nil {
		t.Fatalf("Show after edits: %v", err)
	}
	if view.Body != "I am Ada, and I like precision." || view.Schema != "mine/v3" {
		t.Fatalf("final view = %+v, want body/schema from both edits", view)
	}
}
