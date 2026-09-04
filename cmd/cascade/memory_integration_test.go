//go:build !windows && integration

// Purpose: the end-to-end proof for `cascade memory`: the real cobra
//
//	commands, the real internal/client SDK, the real internal/rpc
//	Registry/Handler pipeline and the real memory.* handler over a real
//	unix socket, with a real file store on disk. It is the executable
//	form of the CLI script the contract asks for — exit codes and output
//	shapes for all four verbs against a live daemon — expressed in Go
//	because this module carries no testscript dependency and adding one
//	would need the license gate (see the ticket journal).
//
// Constraints: build-tagged "integration" because it imports "net"/
//
//	"net/http", which the no-network unit lane forbids (Art.7.2).
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-3 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// startMemoryDaemon serves the real memory.* namespace over a real unix
// socket and returns memoryDeps whose Call is the PRODUCTION one, so the
// test exercises the shipped transport rather than a stand-in.
func startMemoryDaemon(t *testing.T) (memoryDeps, string) {
	t.Helper()
	// Short directory: a unix socket path is capped near 104 bytes and
	// t.TempDir() embeds the test name. The memory STORE still lives in
	// t.TempDir() — only the socket needs the short path.
	sockDir, err := os.MkdirTemp("", "memcli")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	storeRoot := t.TempDir()
	registry := rpc.NewRegistry()
	registerMemoryHandler(registry, fakeMemoryPaths{root: storeRoot}, runtime.SystemClock{}, nil, nil)

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

	return memoryDeps{
		Paths:   fakeMemoryPaths{root: sockDir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Call:    clientMemoryCall,
	}, storeRoot
}

// runMemory executes one verb against the live daemon and returns stdout.
func runMemory(t *testing.T, deps memoryDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryCmd(deps)
	flags := cmd.PersistentFlags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), err
}

// TestMemoryCLIEndToEnd walks the whole surface against a live daemon:
// remember, list, recall, a dry-run forget, a real forget, and the state
// of the store on disk afterwards.
func TestMemoryCLIEndToEnd(t *testing.T) {
	deps, storeRoot := startMemoryDaemon(t)

	out, err := runMemory(t, deps, "remember", "a note about widgets", "--name", "widget-note")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if strings.TrimSpace(out) != "project/widget-note" {
		t.Fatalf("remember printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, "memory", "project", "widget-note.md")); err != nil {
		t.Fatalf("the record did not reach the disk: %v", err)
	}

	if out, err = runMemory(t, deps, "list"); err != nil || !strings.Contains(out, "project/widget-note") {
		t.Fatalf("list = %q, err %v", out, err)
	}
	if out, err = runMemory(t, deps, "recall", "widgets"); err != nil || !strings.Contains(out, "project/widget-note") {
		t.Fatalf("recall = %q, err %v", out, err)
	}
	if out, err = runMemory(t, deps, "recall", "nothing-matches-this"); err != nil || !strings.Contains(out, "no records") {
		t.Fatalf("empty recall = %q, err %v", out, err)
	}

	if out, err = runMemory(t, deps, "forget", "project/widget-note", "--dry-run"); err != nil {
		t.Fatalf("forget --dry-run: %v", err)
	}
	if !strings.Contains(out, "would forget") {
		t.Fatalf("dry run printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, "memory", "project", "widget-note.md")); err != nil {
		t.Fatalf("a dry run removed the record: %v", err)
	}

	if out, err = runMemory(t, deps, "forget", "project/widget-note"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(out, "forgot project/widget-note") {
		t.Fatalf("forget printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, "memory", "project", "widget-note.md.tombstone")); err != nil {
		t.Fatalf("no tombstone after forget: %v", err)
	}
	if out, err = runMemory(t, deps, "list"); err != nil || !strings.Contains(out, "no records") {
		t.Fatalf("list after forget = %q, err %v", out, err)
	}
}

// TestMemoryCLIJSONEnvelopeOverTheWire proves the --json contract holds on
// a real round trip, not only against a recorded result.
func TestMemoryCLIJSONEnvelopeOverTheWire(t *testing.T) {
	deps, _ := startMemoryDaemon(t)
	if _, err := runMemory(t, deps, "remember", "json body", "--name", "json-note", "--type", "user"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	out, err := runMemory(t, deps, "recall", "json", "--json")
	if err != nil {
		t.Fatalf("recall --json: %v", err)
	}
	var envelope struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Data    struct {
			Units []memory.MemoryEntry `json:"units"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("--json output is not an envelope: %v (%s)", err, out)
	}
	if envelope.Version != 1 || !envelope.OK || len(envelope.Data.Units) != 1 {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Data.Units[0].Body != "json body" {
		t.Errorf("unit body = %q", envelope.Data.Units[0].Body)
	}
}

// TestMemoryCLIErrorsOverTheWire covers the failure exit paths: an
// unreachable daemon, an unknown kind and an absent address.
func TestMemoryCLIErrorsOverTheWire(t *testing.T) {
	deps, _ := startMemoryDaemon(t)
	if _, err := runMemory(t, deps, "remember", "x", "--type", "bogus"); err == nil {
		t.Error("an invalid --type must fail")
	}
	if _, err := runMemory(t, deps, "forget", "project/absent"); err == nil {
		t.Error("forgetting an absent address must fail")
	}

	dead := deps
	dead.Paths = fakeMemoryPaths{root: filepath.Join(t.TempDir(), "nowhere")}
	_, err := runMemory(t, dead, "list")
	if err == nil {
		t.Fatal("an unreachable daemon must fail")
	}
	if strings.Contains(err.Error(), string(filepath.Separator)+"nowhere") {
		t.Errorf("the unreachable-daemon error leaked a machine path: %v", err)
	}
}

// TestMemoryCLIIsNonInteractive asserts the automation-parity claim
// (§5.8): CASCADE_NO_INPUT changes nothing, because no verb ever prompts.
func TestMemoryCLIIsNonInteractive(t *testing.T) {
	deps, _ := startMemoryDaemon(t)
	t.Setenv("CASCADE_NO_INPUT", "1")
	out, err := runMemory(t, deps, "remember", "non-interactive", "--name", "quiet-note")
	if err != nil {
		t.Fatalf("remember under CASCADE_NO_INPUT: %v", err)
	}
	if strings.TrimSpace(out) != "project/quiet-note" {
		t.Errorf("stdout = %q", out)
	}
	// The dialer is the SDK's own; naming it here keeps the import honest
	// about what this file proves it uses.
	var _ client.DialFunc = client.UnixDialer
}

// TestMemoryReviewCLIEndToEnd is the both-directions wiring proof for the
// review queue: the CLI verb reaches memory.review.list/act over a real
// socket, and the actions land in the real candidate ledger on disk.
//
// Deleting the review registration from registerMemoryHandler fails this
// with "unknown method", and deleting the verb from newMemoryCmd fails it
// with "unknown command".
func TestMemoryReviewCLIEndToEnd(t *testing.T) {
	deps, storeRoot := startMemoryDaemon(t)
	base := filepath.Join(storeRoot, "memory")
	clock := runtime.SystemClock{}
	ledger := memory.NewFileCandidateLedger(base, memory.NewFileStore(base, clock), clock, nil)

	draft := func(name string) memory.MemoryEntry {
		return memory.MemoryEntry{
			Name: name, Kind: memory.KindProject, Description: "d", Body: "b\n",
			ScopeRef: "global", Confidence: 0.5,
			Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-1"},
		}
	}
	if _, err := ledger.Observe(context.Background(),
		memory.Observation{SessionID: "s-1", Draft: draft("below")}); err != nil {
		t.Fatalf("seeding a candidate: %v", err)
	}

	out, err := runMemory(t, deps, "review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !strings.Contains(out, "project/below") {
		t.Fatalf("the queue did not reach the terminal: %q", out)
	}

	if out, err = runMemory(t, deps, "review", "project/below", "--defer-days", "2"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if !strings.Contains(out, "deferred project/below") {
		t.Fatalf("defer printed %q", out)
	}
	if out, err = runMemory(t, deps, "review"); err != nil {
		t.Fatalf("review after defer: %v", err)
	}
	if strings.Contains(out, "project/below") || !strings.Contains(out, "hidden by a defer") {
		t.Fatalf("the deferred candidate was not hidden and counted: %q", out)
	}

	if out, err = runMemory(t, deps, "review", "project/below", "--auto-approve"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !strings.Contains(out, "promoted project/below") {
		t.Fatalf("approve printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(base, "project", "below.md")); err != nil {
		t.Fatalf("an approved candidate did not become a durable record: %v", err)
	}
	if out, err = runMemory(t, deps, "review", "--section", "promoted"); err != nil {
		t.Fatalf("review --section promoted: %v", err)
	}
	if !strings.Contains(out, "project/below") {
		t.Fatalf("the promotion is not reviewable: %q", out)
	}

	if _, err = runMemory(t, deps, "review", "project/absent", "--auto-skip"); err == nil {
		t.Error("skipping an absent candidate must fail")
	}
}
