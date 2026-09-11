//go:build !windows && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_FleetJournalShowReachableOverRealSocket proves
// fleet.journal_show is reachable on the daemon's own socket, closing the
// R-14.223 gap: internal/fleet/journal.RegisterHandlers previously had no
// production caller anywhere in the tree (internal/build/testonly-
// allow.json carried the exemption). It seeds a real entry through the
// SAME journal.SQLiteStore/DefaultNamespace buildRPCServer's composition
// wiring (daemon.RegisterFleetJournalHandler, internal/daemon/
// journal_rpc.go) now reads, dials fleet.journal_show over a REAL unix
// socket with a REAL HTTP client (never calling the handler function
// in-process, which would prove nothing about whether the composition
// root actually registered it), and asserts the seeded entry comes back.
func TestBuildRPCServer_FleetJournalShowReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	store := storetest.NewMemStore()

	// Seed one real entry through the SAME store/namespace
	// daemon.RegisterFleetJournalHandler builds its journal.SQLiteStore
	// over (journal.DefaultNamespace) — the identical pairing
	// cmd/cascade/daemon_resume.go's wireResumeScan already uses in
	// production.
	seed := journal.New(store, clock, journal.DefaultNamespace)
	const entityID = "journal-e2e-entity"
	if _, err := seed.Append(context.Background(), entityID, journal.KindIntent, "op-1", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	dir, err := os.MkdirTemp("", "journale2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: t.TempDir()}, nil, store)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "journal-e2e",
		"method":  journal.MethodShow,
		"params":  map[string]any{"entity_id": entityID},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s -> %d, want 200", rpc.RPCPath, resp.StatusCode)
	}

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Entries []journal.Entry `json:"entries"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("fleet.journal_show over the daemon socket returned an error: %d %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(envelope.Result.Entries))
	}
	if envelope.Result.Entries[0].OperationID != "op-1" {
		t.Errorf("Entries[0].OperationID = %q, want %q", envelope.Result.Entries[0].OperationID, "op-1")
	}
}
