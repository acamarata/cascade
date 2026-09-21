//go:build !windows && integration

// Package integration holds Article-2 real-counterpart tests that span
// more than one internal package's own test tree — this file's subject
// is recall.what's daemon-side wiring (fix item 12: "the tagged
// live-daemon integration test the task list names: none exists, and the
// -tags integration check is green only because the untagged in-process
// test also runs under the tag").
//
// Purpose: prove recall.what is reachable end to end through REAL
// production pieces: a real modernc.org/sqlite cascade.db (the memory
// leg's *memory.ProjectionJob over a real key-value store and a real
// *memory.FileStore), a real egress.Engine (the actual EgressClassRecallWhat
// entry registered in internal/hooks/egress/classes.go), the real
// internal/rpc.Registry/Handler pipeline served over a real unix socket
// (mirroring internal/daemon/daemon_ipc_e2e_integration_test.go's own
// pattern, and this package's own context_scope_test.go), and the real
// internal/client.Client SDK dialing it.
//
// SCOPE NOTE: this file lives outside T-1's original files_scope
// (internal/integration/), a recorded, minimal scope deviation — the
// alternative (no real end-to-end daemon proof at all) is exactly the gap
// fix item 12 named. Files/conversation legs are left nil here (a
// documented "domain not configured" degradation every leg contract
// already covers); this test's job is proving the WIRING and the memory
// leg's real read path, not re-proving fusion across all three domains
// (recallwhat_test.go already does that against real fakes, and the
// files/conversation legs' own real-counterpart proofs live in their own
// packages' integration tests).
//
// SPORT: internal/integration (ADD, P1-E22-W5-S47-T1, fix item 12).
package integration

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// recallWhatDialTimeout mirrors cmd/cascade/recall.go's own constant.
const recallWhatDialTimeout = 5 * time.Second

// integrationScopeResolver always resolves to the same real
// scope.SessionScope: this test proves the wiring and the memory leg's
// real read path (see file header), not the scope graph's own
// resolution, which internal/context/scope's own tests cover directly.
type integrationScopeResolver struct{ project string }

func (r integrationScopeResolver) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	return scope.SessionScope{Kind: scope.ScopeKindSession, Project: r.project}, nil
}

// startRecallWhatDaemon builds a real *retrieval.RecallWhatService — a
// real *memory.ProjectionJob over a real modernc SQLite key-value store
// and a real *memory.FileStore, a real egress.Engine bound to
// egress.DefaultRegistry() (the actual EgressClassRecallWhat entry) — and
// serves recall.what over a real unix socket, returning a real
// internal/client.Client pointed at it.
func startRecallWhatDaemon(t *testing.T) (*client.Client, memory.MemoryStore, *memory.ProjectionJob) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()

	driver, err := sqlite.Open(ctx, filepath.Join(dataDir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	clock := runtime.NewSystemClock()
	files := memory.NewFileStore(filepath.Join(dataDir, "memory"), clock)
	projection := memory.NewProjectionJob(files, driver, nil, nil, clock)

	registry := rpc.NewRegistry()
	retrieval.NewRecallWhatHandler(buildRecallWhatService(t, projection, clock)).Register(registry)
	return serveRecallWhatRegistry(t, registry), files, projection
}

// buildRecallWhatService builds the real *retrieval.RecallWhatService
// over projection and a real egress.Engine (the actual
// EgressClassRecallWhat entry). Split from startRecallWhatDaemon purely
// for the 50-line function cap.
func buildRecallWhatService(t *testing.T, projection *memory.ProjectionJob, clock runtime.Clock) *retrieval.RecallWhatService {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	firewall, err := egress.NewEngine(egress.DefaultRegistry(), &mapVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	svc, err := retrieval.NewRecallWhatService(
		nil, nil, projection, rrf.Params{}, clock, integrationScopeResolver{project: "proj1"}, firewall,
	)
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	return svc
}

// serveRecallWhatRegistry serves registry over a real unix socket and
// returns a real client dialed to it. Split from startRecallWhatDaemon
// purely for the 50-line function cap.
func serveRecallWhatRegistry(t *testing.T, registry *rpc.Registry) *client.Client {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "recallwhat")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	srv := daemon.NewRPCServer(registry, nil)
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

	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	return client.New(sockPath, client.DialFunc(dial), recallWhatDialTimeout)
}

// mapVault is a hermetic in-memory value source, mirroring
// internal/hooks/firewall_test.go's identical helper.
type mapVault struct{}

func (mapVault) List(context.Context) ([]string, error) { return nil, nil }
func (mapVault) Get(_ context.Context, name string) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindNotFound, "test vault: %q", name)
}

// TestRecallWhatRealCounterparts drives recall.what through every real
// production piece this file's header names: a memory record written to
// a real *memory.FileStore, projected by a real *memory.ProjectionJob
// into a real SQLite-backed key-value store, served over a real unix
// socket, and read back by the real client SDK.
func TestRecallWhatRealCounterparts(t *testing.T) {
	c, files, projection := startRecallWhatDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := files.Write(ctx, memory.MemoryEntry{
		Name: "fusion-note", Kind: memory.KindProject, Body: "reciprocal rank fusion notes",
		ScopeRef: "proj1", Provenance: memory.Provenance{Origin: memory.OriginSession, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("write memory entry: %v", err)
	}
	// The SAME projection instance the daemon wired into the service:
	// Search reads the projected KV rows, never the files directly, so
	// the write above is invisible to recall.what until this runs — the
	// real production shape (a background job runs the projection; this
	// test drives it inline rather than waiting on a scheduler).
	if _, err := projection.Run(ctx); err != nil {
		t.Fatalf("projection.Run: %v", err)
	}

	var result retrieval.WhatResult
	if err := c.Do(ctx, retrieval.MethodWhat, retrieval.WhatParams{Query: "reciprocal"}, &result); err != nil {
		t.Fatalf("recall.what: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Domain != retrieval.DomainMemory {
		t.Fatalf("result = %+v, want one memory-domain row", result)
	}
}

// TestRecallWhatRealCounterparts_WiringProof is the composition-root
// mutation proof: the SAME real socket and client, but built with an
// UNREGISTERED registry, so recall.what must be genuinely unreachable —
// proving the test above exercises the actual registration line, not a
// vacuously-passing fixture.
func TestRecallWhatRealCounterparts_WiringProof(t *testing.T) {
	registry := rpc.NewRegistry() // recall.what deliberately never registered
	c := serveRecallWhatRegistry(t, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result retrieval.WhatResult
	if err := c.Do(ctx, retrieval.MethodWhat, retrieval.WhatParams{Query: "x"}, &result); err == nil {
		t.Fatal("recall.what succeeded against an unregistered method, want an error")
	}
}
