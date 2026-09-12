//go:build !windows && integration

// Purpose: the Art.2 real-counterpart proof for the review-queue in-chat
//
//	surface: a live daemon serving the genuine internal/memory/review
//	Handler and internal/memory Handler over a real unix socket, with
//	HandleReviewChatCommand dispatching through client adapters built on
//	the real internal/client transport.
//
// Constraints: build-tagged "integration"; _test.go files are exempt from
//
//	the plugins-providers-boundary depguard rule, so this file may import
//	internal/** to stand up the real daemon side.
//
// SPORT: plugins/cascade-pa:review-chat (ADD) — P1-E22-W5-S47-T4.
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
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// realReviewChatClient adapts internal/client's transport to
// ReviewChatClient, duplicating the wire shapes rather than importing
// internal/memory/review's own (unexported RPC decode targets in spirit —
// they are exported, but this keeps the plugin-side shape independent,
// matching soul_chat_integration_test.go's identical practice).
type realReviewChatClient struct{ rpc *client.Client }

type wireReviewCandidate struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Sessions int    `json:"sessions"`
	RefCount int    `json:"ref_count"`
}

type wireReviewListResult struct {
	Pending []wireReviewCandidate `json:"pending"`
}

type wireReviewActParams struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type wireReviewActResult struct {
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
}

type wireForgetParams struct {
	ID string `json:"id"`
}

type wireForgetResult struct {
	Forgotten bool `json:"forgotten"`
}

func (c *realReviewChatClient) List(ctx context.Context) (ReviewChatListing, error) {
	var res wireReviewListResult
	if err := c.rpc.Do(ctx, review.MethodReviewList, struct{}{}, &res); err != nil {
		return ReviewChatListing{}, err
	}
	out := ReviewChatListing{}
	for _, p := range res.Pending {
		out.Pending = append(out.Pending, ReviewChatCandidate{
			ID: p.ID, Kind: p.Kind, Sessions: p.Sessions, RefCount: p.RefCount,
		})
	}
	return out, nil
}

func (c *realReviewChatClient) Act(ctx context.Context, id, action string) (ReviewChatActResult, error) {
	var res wireReviewActResult
	params := wireReviewActParams{ID: id, Action: action}
	if err := c.rpc.Do(ctx, review.MethodReviewAct, params, &res); err != nil {
		return ReviewChatActResult{}, err
	}
	return ReviewChatActResult{Action: res.Action, Changed: res.Changed}, nil
}

func (c *realReviewChatClient) Forget(ctx context.Context, id string) (ReviewChatForgetResult, error) {
	var res wireForgetResult
	if err := c.rpc.Do(ctx, memory.MethodForget, wireForgetParams{ID: id}, &res); err != nil {
		return ReviewChatForgetResult{}, err
	}
	return ReviewChatForgetResult{Forgotten: res.Forgotten}, nil
}

// reviewDaemonFixture bundles the live socket client with the store base,
// so the test can seed candidates and records directly against the same
// tree the daemon serves.
type reviewDaemonFixture struct {
	rpc   *client.Client
	base  string
	clock runtime.Clock
}

// startReviewDaemon serves the real memory.* and memory.review.*
// namespaces over a real unix socket, matching
// cmd/cascade/daemon_unix_run_memory.go's composition exactly (store,
// candidate ledger over the same base, review queue over that ledger).
func startReviewDaemon(t *testing.T) reviewDaemonFixture {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "reviewchat")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	base := t.TempDir()
	clock := runtime.SystemClock{}
	registry := rpc.NewRegistry()
	store := memory.NewFileStore(base, clock)
	memory.NewHandler(store, clock).Register(registry)
	ledger := memory.NewFileCandidateLedger(base, store, clock, nil)
	review.NewHandler(review.NewQueue(ledger, clock, nil)).Register(registry)

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
	return reviewDaemonFixture{
		rpc:   client.New(sockPath, client.UnixDialer, 5*time.Second),
		base:  base,
		clock: clock,
	}
}

// seedPendingCandidate writes one below-threshold observation directly
// against the daemon's own store tree, so `next` has something real to
// list.
func seedPendingCandidate(t *testing.T, f reviewDaemonFixture, name string) {
	t.Helper()
	store := memory.NewFileStore(f.base, f.clock)
	ledger := memory.NewFileCandidateLedger(f.base, store, f.clock, nil)
	draft := memory.MemoryEntry{
		Name: name, Kind: memory.KindProject, Description: "d", Body: "body",
		ScopeRef: "global", Confidence: 0.8,
		Provenance: memory.Provenance{Origin: memory.OriginSession},
	}
	if _, err := ledger.Observe(context.Background(), memory.Observation{SessionID: "s1", Draft: draft}); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
}

// seedForgettableRecord writes a standalone durable record, independent of
// the review ledger, so `forget` has something real to remove.
func seedForgettableRecord(t *testing.T, f reviewDaemonFixture, name string) {
	t.Helper()
	store := memory.NewFileStore(f.base, f.clock)
	entry := memory.MemoryEntry{
		Name: name, Kind: memory.KindProject, Description: "d", Body: "body",
		ScopeRef: "global", Confidence: 0.8,
		Provenance: memory.Provenance{Origin: memory.OriginSession},
	}
	if err := store.Write(context.Background(), entry); err != nil {
		t.Fatalf("seed forgettable record: %v", err)
	}
}

// TestReviewQueueChatRPC is the contract's required integration proof:
// review.list returns a seeded candidate, and approve/skip (via
// review.act) and forget (via memory.forget) each land in the ledger
// through the audited API.
func TestReviewQueueChatRPC(t *testing.T) {
	fx := startReviewDaemon(t)
	withReviewChatClient(t, &realReviewChatClient{rpc: fx.rpc})
	ctx := context.Background()

	seedPendingCandidate(t, fx, "note-a")
	reply, matched, err := HandleReviewChatCommand(ctx, "/memory review next")
	if err != nil || !matched {
		t.Fatalf("next: matched=%v err=%v", matched, err)
	}
	if !strings.Contains(reply, "project/note-a") {
		t.Fatalf("next reply = %q, want it to name project/note-a", reply)
	}

	skipReply, matched, err := HandleReviewChatCommand(ctx, "/memory review skip project/note-a")
	if err != nil || !matched || !strings.Contains(skipReply, "no change") {
		t.Fatalf("skip: reply=%q matched=%v err=%v", skipReply, matched, err)
	}

	approveReply, matched, err := HandleReviewChatCommand(ctx, "/memory review approve project/note-a")
	if err != nil || !matched || !strings.Contains(approveReply, "applied") {
		t.Fatalf("approve: reply=%q matched=%v err=%v", approveReply, matched, err)
	}

	seedForgettableRecord(t, fx, "note-b")
	forgetReply, matched, err := HandleReviewChatCommand(ctx, "/memory review forget project/note-b")
	if err != nil || !matched || !strings.Contains(forgetReply, "forgotten") {
		t.Fatalf("forget: reply=%q matched=%v err=%v", forgetReply, matched, err)
	}

	store := memory.NewFileStore(fx.base, fx.clock)
	if exists, err := store.Exists(ctx, memory.KindProject, "note-b"); err != nil || exists {
		t.Fatalf("note-b Exists after forget = (%v, %v), want (false, nil)", exists, err)
	}
}
