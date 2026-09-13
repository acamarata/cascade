//go:build !windows && integration

// Purpose: FIX-retrieval-leg-wiring.md's end-to-end proof for the DAEMON
// composition root (registerRecallHandler, daemon_unix_handlers.go): with
// a real chunk written into the real store buildRPCServer is handed, and
// a matching catalog entry, a live recall.query call over a real unix
// socket must return that chunk's corpus — never "no results" and never
// KindUnavailable's "no retrieval leg is available", both of which were
// the only two reachable answers before this fix wired
// internal/retrieval.NewLeg into this composition root.
//
// Constraints: build-tagged "integration" (imports "net"/"net/http",
// which the no-network unit lane forbids, Art.7.2), following
// recall_index_integration_test.go's exact pattern.
//
// SPORT: cmd.cascade.cmd.recall (FIX, full-text leg wiring, daemon side).
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestRecallQueryFusesRealHitsOverTheLiveDaemon seeds one real chunk
// through the production write path (retrieval.NewIndex.Write) into the
// same store buildRPCServer is handed, writes the matching catalog entry,
// starts the real daemon over a real socket, and drives the real cobra
// command + SDK client against it.
func TestRecallQueryFusesRealHitsOverTheLiveDaemon(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "recallftscli")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	root := t.TempDir()
	paths := fakeMemoryPaths{root: root}
	clock := runtime.SystemClock{}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	driver, err := sqlite.Open(context.Background(), filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	c := corpus.Corpus{
		ID: "handbook", ScopeRef: "project/example",
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal, Trust: corpus.TrustTrusted,
	}
	body := "reciprocal rank fusion combines ranked lists from every retrieval leg"
	chunk := retrieval.Chunk{
		ID: retrieval.ChunkID([]byte(body)), Path: "handbook/fusion.md",
		Content: []byte(body), Lang: "markdown", EndByte: len(body),
	}
	idx, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("retrieval.NewIndex: %v", err)
	}
	if err := idx.Write(context.Background(), c.ID, []retrieval.Chunk{chunk}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	indexDir := filepath.Join(paths.DataDir(), "retrieval")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatalf("mkdir retrieval dir: %v", err)
	}
	doc := recall.CatalogDoc{
		Version: recall.CatalogVersion,
		Corpora: []corpus.Corpus{c},
		Records: []corpus.Record{{
			ID: chunk.ID, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, recall.CatalogFileName), raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	bus := events.New(storetest.NewMemStore(), clock)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	settings := daemon.Settings{SocketPath: filepath.Join(sockDir, "daemon.sock")}
	server, _, _, err := buildRPCServer(bus, clock, logger, settings, paths, nil, driver)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	ln, err := net.Listen("unix", settings.SocketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server.ReadHeaderTimeout = 5 * time.Second
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	deps := recallDeps{
		Paths:   fakeMemoryPaths{root: sockDir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Call:    clientRecallCall,
	}
	out, err := runRecallAgainstDaemon(t, deps, "reciprocal rank fusion", "--scope", string(c.ScopeRef))
	if err != nil {
		t.Fatalf("recall.query over the live daemon: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "no results") {
		t.Fatalf("real content was indexed but the daemon reported no results:\n%s", out)
	}
	if !strings.Contains(out, c.ID) {
		t.Fatalf("output does not name the indexed corpus %q:\n%s", c.ID, out)
	}
}
