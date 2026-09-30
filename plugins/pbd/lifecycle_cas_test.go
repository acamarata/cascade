// Package pbd (lifecycle_cas_test.go): end-to-end proof that AppendIf makes
// a lifecycle claim atomic — across goroutines (TestClaimConcurrentSingleWinner)
// and across processes (TestClaimCrossProcessSingleWinner), plus the
// operation-id idempotency contract (TestLifecycleOperationIdempotent).
// TestMain re-execs this same test binary as the cross-process helper: no
// separate binary, no net (os/exec only), no integration build tag.
// SPORT: plugins/pbd lifecycle_appendif (ADD) — P1-PBD-07.
package pbd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

const (
	claimHelperIDEnv   = "PBD_CLAIM_HELPER_ID"
	claimHelperRootEnv = "PBD_CLAIM_HELPER_ROOT"
	claimHelperOpEnv   = "PBD_CLAIM_HELPER_OP"
	claimHelperGateEnv = "PBD_CLAIM_HELPER_GATE"
)

// TestMain intercepts a re-exec'd helper invocation (claimHelperIDEnv set)
// before any test runs, so TestClaimCrossProcessSingleWinner's children
// never execute go test's normal suite.
func TestMain(m *testing.M) {
	if os.Getenv(claimHelperIDEnv) != "" {
		os.Exit(runClaimHelper())
	}
	os.Exit(m.Run())
}

// claimHelperTree returns the single-ticket tree every claim contender
// (goroutine or subprocess) claims against; deterministic, no disk read.
func claimHelperTree(id string) *pews.Tree {
	return &pews.Tree{Phase: "P1", Tickets: []pews.TicketRecord{{Ticket: pews.Ticket{ID: id}}}}
}

// runClaimHelper is the re-exec'd child process body: wait for the gate
// file (forcing contention with its sibling), then attempt one Claim.
// Exit 0 means it won; exit 1 means it lost or errored.
func runClaimHelper() int {
	id := os.Getenv(claimHelperIDEnv)
	root := os.Getenv(claimHelperRootEnv)
	op := os.Getenv(claimHelperOpEnv)
	gate := os.Getenv(claimHelperGateEnv)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "gate file never appeared")
			return 1
		}
		time.Sleep(time.Millisecond)
	}
	js := NewFileJournalStore(root, nil)
	if _, err := pews.Claim(context.Background(), claimHelperTree(id), js, id, op); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// TestClaimConcurrentSingleWinner is acceptance[0]: sixteen goroutines
// claim one ticket at once through a barrier (every goroutine reaches the
// Claim call before any of them runs it), so the sequence compare inside
// AppendIf's lock — not scheduling luck — decides the single winner.
func TestClaimConcurrentSingleWinner(t *testing.T) {
	root := t.TempDir()
	js := NewFileJournalStore(root, nil)
	tree := claimHelperTree("P1-E00-W0-S00-T1")
	id := tree.Tickets[0].Ticket.ID
	const n = 16

	results := runConcurrentClaims(js, tree, n)
	successes, conflicts := tallyClaimResults(t, id, results)
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1", successes)
	}
	if conflicts != n-1 {
		t.Errorf("conflicts = %d, want exactly %d", conflicts, n-1)
	}
	assertSingleClaimEntry(t, js, id)
}

// runConcurrentClaims launches n goroutines behind a barrier (every one
// reaches the Claim call before any of them runs it), so the sequence
// compare inside AppendIf's lock — not scheduling luck — decides the
// single winner, and returns each goroutine's Claim error in order.
func runConcurrentClaims(js *FileJournalStore, tree *pews.Tree, n int) []error {
	id := tree.Tickets[0].Ticket.ID
	var readyCount int32
	ready := make(chan struct{})
	start := make(chan struct{})
	results := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if atomic.AddInt32(&readyCount, 1) == int32(n) {
				close(ready)
			}
			<-start
			_, err := pews.Claim(context.Background(), tree, js, id, fmt.Sprintf("op-%d", i))
			results[i] = err
		}()
	}
	<-ready
	close(start)
	wg.Wait()
	return results
}

// tallyClaimResults counts nil (success) and KindConflict results, failing
// t on any other error shape. A conflict must carry one of the two exact
// messages a raced Claim can legitimately produce (isClaimConflict) —
// identity, not Kind alone (cascade's *Error.Is compares Kind only, so it
// cannot tell either shape from an unrelated conflict).
func tallyClaimResults(t *testing.T, id string, results []error) (successes, conflicts int) {
	t.Helper()
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case isClaimConflict(id, err):
			conflicts++
		default:
			t.Errorf("claim: unexpected error %v", err)
		}
	}
	return successes, conflicts
}

// isClaimConflict reports whether err is exactly one of the two
// KindConflict shapes a raced Claim can return: AppendIf's stale-sequence
// refusal (a late contender still sees expectedSeq 0 against the winner's
// one landed entry), or applyClaim's already-claimed transition refusal (a
// later contender's own Replay snapshot already shows the winner's entry).
func isClaimConflict(id string, err error) bool {
	cerr, ok := err.(*cascade.Error)
	if !ok || cerr.Kind != cascade.KindConflict {
		return false
	}
	staleSeq := fmt.Sprintf("pbd: entity %q: expected seq 0, have 1", id)
	alreadyClaimed := fmt.Sprintf("pews: lifecycle event %q is not valid from state %q", pews.EventClaim, pews.StateClaimed)
	return cerr.Msg == staleSeq || cerr.Msg == alreadyClaimed
}

// assertSingleClaimEntry replays id's journal and fails t unless it holds
// exactly one entry, a claim.
func assertSingleClaimEntry(t *testing.T, js *FileJournalStore, id string) {
	t.Helper()
	entries, rerr := js.Replay(context.Background(), id)
	if rerr != nil {
		t.Fatalf("Replay: %v", rerr)
	}
	if len(entries) != 1 {
		t.Fatalf("journal has %d entries, want exactly 1", len(entries))
	}
	if entries[0].Event != pews.EventClaim {
		t.Errorf("journal entry event = %q, want %q", entries[0].Event, pews.EventClaim)
	}
}

// TestClaimCrossProcessSingleWinner is acceptance[1]: two re-exec'd
// subprocesses claim the same ticket against one journal root, gated so
// both are running before either can proceed; exactly one exit reports
// success and the journal holds one claim entry.
func TestClaimCrossProcessSingleWinner(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	root := t.TempDir()
	const id = "P1-E00-W0-S00-T2"
	gate := root + ".gate"

	cmd1, buf1 := newClaimHelperCmd(exe, id, root, gate, "a")
	cmd2, buf2 := newClaimHelperCmd(exe, id, root, gate, "b")
	if err := cmd1.Start(); err != nil {
		t.Fatalf("start cmd1: %v", err)
	}
	if err := cmd2.Start(); err != nil {
		t.Fatalf("start cmd2: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let both children reach the gate wait
	if werr := os.WriteFile(gate, []byte("go"), 0o644); werr != nil {
		t.Fatalf("writing gate file: %v", werr)
	}

	successes := tallyExitResults(t, cmd1.Wait(), buf1, cmd2.Wait(), buf2)
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (out1=%s out2=%s)", successes, buf1, buf2)
	}
	assertSingleClaimEntry(t, NewFileJournalStore(root, nil), id)
}

// newClaimHelperCmd builds one re-exec'd claim-helper subprocess, wired to
// gate and its own captured-output buffer.
func newClaimHelperCmd(exe, id, root, gate, opSuffix string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(),
		claimHelperIDEnv+"="+id,
		claimHelperRootEnv+"="+root,
		claimHelperOpEnv+"=op-proc-"+opSuffix,
		claimHelperGateEnv+"="+gate,
	)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	return cmd, &buf
}

// tallyExitResults counts nil (success) exits among the two Wait results,
// failing t on any exec error that is not a plain nonzero exit.
func tallyExitResults(t *testing.T, err1 error, out1 *bytes.Buffer, err2 error, out2 *bytes.Buffer) int {
	t.Helper()
	successes := 0
	for _, res := range []struct {
		err error
		out *bytes.Buffer
	}{{err1, out1}, {err2, out2}} {
		if res.err == nil {
			successes++
			continue
		}
		if _, ok := res.err.(*exec.ExitError); !ok {
			t.Fatalf("unexpected exec error: %v (output: %s)", res.err, res.out)
		}
	}
	return successes
}

// TestLifecycleOperationIdempotent is acceptance[2]: a repeated
// operation_id for the same entity+event returns the existing entry and
// adds nothing; the same operation_id against a different event refuses
// KindConflict, adding nothing either.
func TestLifecycleOperationIdempotent(t *testing.T) {
	root := t.TempDir()
	js := NewFileJournalStore(root, nil)
	ctx := context.Background()
	const id = "P1-E00-W0-S00-T3"

	first, err := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-shared", nil)
	if err != nil {
		t.Fatalf("first AppendIf: %v", err)
	}
	repeat, err := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-shared", nil)
	if err != nil {
		t.Fatalf("repeat AppendIf (same event): %v", err)
	}
	if repeat.EntityID != first.EntityID || repeat.Seq != first.Seq || repeat.Event != first.Event ||
		repeat.OperationID != first.OperationID || repeat.TSUnixNano != first.TSUnixNano {
		t.Errorf("repeat AppendIf = %+v, want identical to first %+v", repeat, first)
	}
	if entries, rerr := js.Replay(ctx, id); rerr != nil || len(entries) != 1 {
		t.Fatalf("Replay after idempotent repeat = %+v, %v, want exactly 1 entry", entries, rerr)
	}

	_, err = js.AppendIf(ctx, id, 1, pews.EventStep, "op-shared", nil)
	wantMsg := fmt.Sprintf("pbd: entity %q: operation_id %q already recorded event %q, not %q", id, "op-shared", pews.EventClaim, pews.EventStep)
	if cerr, ok := err.(*cascade.Error); !ok || cerr.Kind != cascade.KindConflict || cerr.Msg != wantMsg {
		t.Errorf("AppendIf(same operation_id, different event): err = %v, want Kind=KindConflict Msg=%q (the op-id-reuse refusal, not a plain sequence conflict)", err, wantMsg)
	}
	if entries, rerr := js.Replay(ctx, id); rerr != nil || len(entries) != 1 {
		t.Errorf("Replay after refused cross-event reuse = %+v, %v, want still exactly 1 entry", entries, rerr)
	}
}
