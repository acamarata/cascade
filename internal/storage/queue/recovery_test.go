// Purpose: real-storage durability proofs (real providers/sqlite.Driver,
//   t.TempDir(), no network): restart, two Queue instances over one
//   Driver, delete fencing, legacy migration, a REAL SIGKILLed
//   child's committed Tx surviving a crash. fault_test.go/retry_fault_test.go cover Store faults.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

func requireNoErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func openTestDriver(t *testing.T, path string) *sqlite.Driver {
	t.Helper()
	d, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestP1QueueRestart: a ready and a claimed-but-unacked message both
// survive a restart, the claimed one only once its deadline elapses.
func TestP1QueueRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))

	driver1 := openTestDriver(t, path)
	q1 := queue.New(driver1, clock, queue.Config{})
	// FIFO: the first enqueued is what q1 claims below; the second
	// (readyID) stays untouched in the ready queue.
	claimedID, err := q1.Enqueue(ctx, "ns", []byte("claimed-payload"))
	requireNoErr(t, err, "q1 Enqueue claimed")
	readyID, err := q1.Enqueue(ctx, "ns", []byte("ready-payload"))
	requireNoErr(t, err, "q1 Enqueue ready")
	msg, err := q1.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "q1 Dequeue")
	if msg == nil || msg.ID != claimedID {
		t.Fatalf("q1 Dequeue = %+v, want id %q", msg, claimedID)
	}
	requireNoErr(t, driver1.Close(), "driver1.Close")

	driver2 := openTestDriver(t, path)
	q2 := queue.New(driver2, clock, queue.Config{})

	redelivered, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "q2 Dequeue ready message")
	if redelivered == nil || redelivered.ID != readyID {
		t.Fatalf("q2 Dequeue = %+v, want the never-claimed message %q", redelivered, readyID)
	}
	requireNoErr(t, q2.Ack(ctx, "ns", redelivered.Receipt), "q2 Ack ready message")

	stillInvisible, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "q2 Dequeue before deadline")
	if stillInvisible != nil {
		t.Fatalf("q2 Dequeue before claim deadline elapsed = %+v, want nil", stillInvisible)
	}

	clock.Advance(2 * time.Minute)
	expiredClaim, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "q2 Dequeue after deadline")
	if expiredClaim == nil || expiredClaim.ID != claimedID {
		t.Fatalf("q2 Dequeue after claim deadline elapsed = %+v, want redelivery of %q", expiredClaim, claimedID)
	}
	requireNoErr(t, q2.Ack(ctx, "ns", expiredClaim.Receipt), "q2 Ack redelivered message")
}

// TestP1QueueTwoInstances forces a genuine cross-instance CAS race
// deterministically (a removed-CAS mutation once slipped through a
// two-goroutine version undetected, since goroutines rarely raced at the
// real Tx layer). q2.Enqueue's own namespaceLocked call recovers "ns" for
// q2 — caching both messages as ready — while the contended one is still
// unclaimed. q1 drains its own list for real, claiming the contended one;
// q2's Dequeue, on its now-stale cache, must lose every claim CAS — the
// Store-boundary CAS decides this, not a process mutex.
func TestP1QueueTwoInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue-two-instances.db")
	driver := openTestDriver(t, path)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))

	seed := queue.New(driver, clock, queue.Config{})
	_, err := seed.Enqueue(ctx, "ns", []byte("contended"))
	requireNoErr(t, err, "seed Enqueue")

	q1 := queue.New(driver, clock, queue.Config{})
	q2 := queue.New(driver, clock, queue.Config{})
	_, err = q2.Enqueue(ctx, "ns", []byte("q2-warmup")) // recovers "ns" for q2 while both are still ready
	requireNoErr(t, err, "q2 warmup Enqueue")

	contended := drainToContended(ctx, t, q1)

	stale, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "q2 Dequeue acting on stale recovered state")
	if stale != nil {
		t.Fatalf("q2 Dequeue = %+v, want nil — every entry in q2's stale cache must lose its claim CAS", stale)
	}
	requireNoErr(t, q1.Ack(ctx, "ns", contended.Receipt), "q1 Ack contended message")
}

// drainToContended claims/Acks q's ready list (order not guaranteed
// across independently-seeded instances) until it draws "contended".
func drainToContended(ctx context.Context, t *testing.T, q *queue.Queue) *provider.Message {
	t.Helper()
	var contended *provider.Message
	for i := 0; i < 2; i++ {
		msg, err := q.Dequeue(ctx, "ns", time.Minute)
		requireNoErr(t, err, "drain Dequeue")
		if msg == nil {
			t.Fatal("drain Dequeue = nil before finding the contended message")
		}
		if string(msg.Payload) == "contended" {
			contended = msg
		} else {
			requireNoErr(t, q.Ack(ctx, "ns", msg.Receipt), "drain Ack non-contended message")
		}
	}
	if contended == nil {
		t.Fatal("drain never found the contended message")
	}
	return contended
}

// TestQueueAckDeleteFencedByPriorBytes proves delete fencing: tampering with the
// persisted record between claim and Ack must make Ack refuse the delete
// and report KindConflict, never KindTimeout.
func TestQueueAckDeleteFencedByPriorBytes(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()
	q := queue.New(store, clock, queue.Config{})

	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "Enqueue")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue")
	if msg == nil || msg.ID != id {
		t.Fatalf("Dequeue = %+v, want id %q", msg, id)
	}

	requireNoErr(t, store.Put(ctx, "ns", "msg:"+id, []byte("tampered")), "tamper Put")

	err = q.Ack(ctx, "ns", msg.Receipt)
	requireKindMsg(t, err, cascade.KindConflict, fmt.Sprintf("queue.Ack: message %q in namespace %q changed since claim (receipt %q refused)", id, "ns", msg.Receipt), "tampered Ack")
	cur, getErr := store.Get(ctx, "ns", "msg:"+id)
	requireNoErr(t, getErr, "Get after fenced Ack")
	if string(cur) != "tampered" {
		t.Fatalf("record after fenced Ack = %q, want the tampered bytes untouched (fencing must refuse the delete)", cur)
	}
}

// TestQueue_MigratesLegacyBodyOnlyRecord: a seeded legacy record becomes
// deliverable, keeps its attempt count, stays namespaced.
func TestQueue_MigratesLegacyBodyOnlyRecord(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()
	legacyID := "00000000000000000001-aaaaaaaa"
	legacy := make([]byte, 4+len("legacy-payload"))
	binary.BigEndian.PutUint32(legacy[:4], 3)
	copy(legacy[4:], "legacy-payload")
	requireNoErr(t, store.Put(ctx, "ns", "msg:"+legacyID, legacy), "seed legacy record")

	q := queue.New(store, clock, queue.Config{})
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue migrated legacy record")
	if msg == nil || msg.ID != legacyID || string(msg.Payload) != "legacy-payload" {
		t.Fatalf("Dequeue = %+v, want the migrated legacy record %q/legacy-payload", msg, legacyID)
	}
	requireNoErr(t, q.Ack(ctx, "ns", msg.Receipt), "Ack migrated record")

	other := make([]byte, 4+len("other-ns-payload"))
	copy(other[4:], "other-ns-payload")
	requireNoErr(t, store.Put(ctx, "other-ns", "msg:"+legacyID, other), "seed other-namespace legacy record")
	again, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue after other-namespace seed")
	if again != nil {
		t.Fatalf("Dequeue leaked a record from another namespace: %+v", again)
	}
}

// TestQueue_MigrationIdempotentUnderConcurrentInstances lives in fault_test.go.
const queueKillTestEnv = "CASCADE_QUEUE_KILLTEST_DB_PATH"

// TestQueueKillHelperProcess is re-executed as a SEPARATE OS PROCESS by
// spawnAndKillQueueHelper; an unset env var is a no-op ordinary test run.
func TestQueueKillHelperProcess(_ *testing.T) {
	path := os.Getenv(queueKillTestEnv)
	if path == "" {
		return
	}
	ctx := context.Background()
	driver, err := sqlite.Open(ctx, path)
	if err != nil {
		_, _ = os.Stdout.WriteString("OPEN_FAILED\n")
		return
	}
	q := queue.New(driver, runtime.NewFixedClock(time.Unix(1_700_000_000, 0)), queue.Config{})
	if _, err := q.Enqueue(ctx, "ns", []byte("crash-payload")); err != nil {
		_, _ = os.Stdout.WriteString("ENQUEUE_FAILED\n")
		return
	}
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	if err != nil || msg == nil {
		_, _ = os.Stdout.WriteString("DEQUEUE_FAILED\n")
		return
	}
	// The claim transaction has committed to the real sqlite file. Report
	// READY and block — the parent SIGKILLs this process here, before
	// driver.Close() or any further write ever runs.
	_, _ = os.Stdout.WriteString("READY\n")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}

// spawnAndKillQueueHelper starts TestQueueKillHelperProcess, waits for
// READY (a real claim Tx has committed to path), then SIGKILLs it — a
// held-open stdin pipe keeps the child genuinely blocked so Kill() never
// races exit (mirrors internal/jobs/scheduler_resume_kill9_test.go).
func spawnAndKillQueueHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestQueueKillHelperProcess$")
	cmd.Env = append(os.Environ(), queueKillTestEnv+"="+path)
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinWriter.Close() }()
	cmd.Stdin = stdinReader
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	_ = stdinReader.Close()
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "READY\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper process: want READY, got %q", line)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
}

// TestQueueRecoversAfterRealProcessKill is the real-crash-point proof: a
// REAL child process commits a REAL claim transaction to a REAL sqlite
// file and is REAL SIGKILLed before it can close anything; a fresh Queue
// instance recovers that claim as still-inflight, then redelivers it.
func TestQueueRecoversAfterRealProcessKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-kill.db")
	spawnAndKillQueueHelper(t, path)

	driver := openTestDriver(t, path)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	q := queue.New(driver, clock, queue.Config{})
	ctx := context.Background()

	stillClaimed, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue right after recovering from a real kill")
	if stillClaimed != nil {
		t.Fatalf("Dequeue right after recovering a real-killed claim = %+v, want nil", stillClaimed)
	}

	clock.Advance(2 * time.Minute)
	redelivered, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue after the recovered claim's deadline elapsed")
	if redelivered == nil || string(redelivered.Payload) != "crash-payload" {
		t.Fatalf("Dequeue after real-kill recovery = %+v, want redelivery of the crash-payload message", redelivered)
	}
	requireNoErr(t, q.Ack(ctx, "ns", redelivered.Receipt), "Ack recovered message")
}
