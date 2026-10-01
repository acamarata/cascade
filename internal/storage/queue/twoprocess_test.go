// Purpose: TestP1QueueTwoProcessesOneClaim — two REAL OS processes (this
//   test binary re-executed) over one real sqlite file.
//   1. While the first process holds the store, the second process's Open
//      is refused with KindConflict (exact kind and message) and it claims
//      nothing.
//   2. After the first exits, the second opens and claims only messages
//      that were unclaimed or whose claim had expired — never the
//      unexpired claim seeded beforehand, never the message the first
//      process claimed — and no message is claimed by both processes.
//   Scope: providers/sqlite takes an exclusive flock on Open (one writer at
//   a time), so two processes can never overlap inside claimLocked's CAS;
//   that CAS is proven by TestP1QueueTwoInstances (two Queues, one Driver).
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

const (
	twoProcPathEnv = "CASCADE_QUEUE_TWOPROC_DB_PATH"
	twoProcModeEnv = "CASCADE_QUEUE_TWOPROC_MODE"
	twoProcHold    = "hold"
	twoProcContend = "contend"
	twoProcDrain   = "drain"
)

// TestQueueTwoProcessHelperProcess is re-executed as a separate OS process;
// with no env set it is a no-op. Modes: hold (open, claim one message, print
// HOLDING, keep the store until stdin gets a byte), contend (one Open
// attempt, report a refusal, claim only if the Open unexpectedly worked),
// drain (open, claim every claimable message, never Ack).
func TestQueueTwoProcessHelperProcess(_ *testing.T) {
	path, mode := os.Getenv(twoProcPathEnv), os.Getenv(twoProcModeEnv)
	if path == "" {
		return
	}
	ctx := context.Background()
	driver, err := sqlite.Open(ctx, path)
	if err != nil {
		kind, _ := cascade.KindOf(err)
		fmt.Printf("OPEN_REFUSED:%s:%s\n", kind, strconv.Quote(err.Error()))
		return
	}
	defer func() { _ = driver.Close() }()
	q := queue.New(driver, runtime.NewSystemClock(), queue.Config{})
	limit := -1
	if mode == twoProcHold {
		limit = 1
	}
	for n := 0; n != limit; n++ {
		msg, err := q.Dequeue(ctx, "ns", time.Hour)
		if err != nil {
			fmt.Println("DEQUEUE_ERROR:" + err.Error())
			return
		}
		if msg == nil {
			break
		}
		fmt.Println("CLAIMED:" + msg.ID)
	}
	if mode == twoProcHold {
		fmt.Println("HOLDING")
		_, _ = bufio.NewReader(os.Stdin).ReadByte() // parent releases the store
	}
}

// helperProc is one running helper with its piped stdin and stdout.
type helperProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
}

// startHelper launches one helper in mode; a 60s context kills a hung one.
func startHelper(t *testing.T, path, mode string) *helperProc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQueueTwoProcessHelperProcess$")
	cmd.Env = append(os.Environ(), twoProcPathEnv+"="+path, twoProcModeEnv+"="+mode)
	in, err := cmd.StdinPipe()
	requireNoErr(t, err, "StdinPipe")
	out, err := cmd.StdoutPipe()
	requireNoErr(t, err, "StdoutPipe")
	requireNoErr(t, cmd.Start(), "start "+mode+" helper")
	return &helperProc{cmd: cmd, stdin: in, out: bufio.NewReader(out)}
}

// lines reads stdout until stop matches a line (inclusive) or EOF, returning
// every helper-protocol line (test-harness noise is dropped) and failing on
// any *_ERROR line.
func (h *helperProc) lines(t *testing.T, stop string) []string {
	t.Helper()
	var got []string
	for {
		line, err := h.out.ReadString('\n')
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "_ERROR:"):
			t.Errorf("helper reported a failure: %q", line)
		case strings.HasPrefix(line, "CLAIMED:"), strings.HasPrefix(line, "OPEN_REFUSED:"), line == "HOLDING":
			got = append(got, line)
		}
		if err != nil || (stop != "" && line == stop) {
			return got
		}
	}
}

// finish closes stdin (releasing a holder) and requires a clean exit.
func (h *helperProc) finish(t *testing.T, what string) {
	t.Helper()
	_ = h.stdin.Close()
	requireNoErr(t, h.cmd.Wait(), what+" exit")
}

// claimedIDs extracts the ids from CLAIMED lines.
func claimedIDs(lines []string) []string {
	var ids []string
	for _, l := range lines {
		if id, ok := strings.CutPrefix(l, "CLAIMED:"); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// twoProcSeed is the seeded queue: one unexpired claim, two expired claims
// and three never-claimed messages.
type twoProcSeed struct {
	live      string
	liveBytes []byte
	expired   []string
	unclaimed []string
}

// seedTwoProc builds the seed on a real sqlite file, then closes the file.
// The expired claims come from a Queue whose injected clock is an hour in
// the past, so no test ever sleeps.
func seedTwoProc(t *testing.T, path string) twoProcSeed {
	t.Helper()
	ctx := context.Background()
	d := openTestDriver(t, path)
	now := time.Now()
	q0 := queue.New(d, runtime.NewSystemClock(), queue.Config{})
	var ids []string
	for i := 0; i < 6; i++ {
		id, err := q0.Enqueue(ctx, "ns", []byte(fmt.Sprintf("payload-%d", i)))
		requireNoErr(t, err, "seed Enqueue")
		ids = append(ids, id)
	}
	live, err := q0.Dequeue(ctx, "ns", time.Hour) // unexpired: real clock, one hour out
	requireNoErr(t, err, "seed live claim")
	past := queue.New(d, runtime.NewFixedClock(now.Add(-time.Hour)), queue.Config{})
	var expired []string
	for i := 0; i < 2; i++ {
		m, err := past.Dequeue(ctx, "ns", time.Minute) // deadline 59 minutes ago
		requireNoErr(t, err, "seed expired claim")
		expired = append(expired, m.ID)
	}
	if live == nil || live.ID != ids[0] || expired[0] != ids[1] || expired[1] != ids[2] {
		t.Fatalf("seed shape wrong: live=%+v expired=%v ids=%v", live, expired, ids)
	}
	raw, err := d.Get(ctx, "ns", "msg:"+live.ID)
	requireNoErr(t, err, "read live claim bytes")
	requireNoErr(t, d.Close(), "close seed driver")
	return twoProcSeed{live: live.ID, liveBytes: raw, expired: expired, unclaimed: ids[3:]}
}

// TestP1QueueTwoProcessesOneClaim is the two-process proof described in the
// file header.
func TestP1QueueTwoProcessesOneClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-two-proc.db")
	seed := seedTwoProc(t, path)

	first := startHelper(t, path, twoProcHold)
	firstLines := first.lines(t, "HOLDING")
	firstClaims := claimedIDs(firstLines)
	if len(firstClaims) != 1 || firstLines[len(firstLines)-1] != "HOLDING" {
		t.Fatalf("first process: want exactly one claim then HOLDING, got %q", firstLines)
	}

	second := startHelper(t, path, twoProcContend)
	contendLines := second.lines(t, "")
	second.finish(t, "contending process")
	requireRefusedByLock(t, contendLines, path)

	first.finish(t, "first process")

	drain := startHelper(t, path, twoProcDrain)
	drainClaims := claimedIDs(drain.lines(t, ""))
	drain.finish(t, "second process after the first exited")

	want := append(append([]string(nil), seed.expired...), seed.unclaimed...)
	want = withoutID(want, firstClaims[0])
	sort.Strings(want)
	if strings.Join(drainClaims, ",") != strings.Join(want, ",") {
		t.Fatalf("second process claimed %v, want exactly %v (unclaimed or expired, minus what the first process holds)", drainClaims, want)
	}
	for _, id := range drainClaims {
		if id == seed.live || id == firstClaims[0] {
			t.Fatalf("second process claimed %q, which is under an unexpired claim", id)
		}
	}
	requireLiveClaimUntouched(t, path, seed)
}

// requireRefusedByLock asserts the contender saw exactly one OPEN_REFUSED
// line, KindConflict with the driver's lock message, and claimed nothing.
func requireRefusedByLock(t *testing.T, lines []string, path string) {
	t.Helper()
	if len(claimedIDs(lines)) != 0 || len(lines) != 1 {
		t.Fatalf("contending process must report only a refused Open and claim nothing, got %q", lines)
	}
	kindPart, quoted, _ := strings.Cut(strings.TrimPrefix(lines[0], "OPEN_REFUSED:"), ":")
	msg, err := strconv.Unquote(quoted)
	requireNoErr(t, err, "unquote refusal message")
	if kindPart != cascade.KindConflict.String() {
		t.Fatalf("second Open kind = %q, want %q (message %q)", kindPart, cascade.KindConflict, msg)
	}
	wantPrefix := cascade.KindConflict.String() + ": sqlite: exclusive lock held by another process on "
	if !strings.HasPrefix(msg, wantPrefix) || !strings.Contains(msg, filepath.Base(path)) {
		t.Fatalf("second Open message = %q, want prefix %q naming %s", msg, wantPrefix, filepath.Base(path))
	}
}

// requireLiveClaimUntouched reopens the file and checks the seeded
// unexpired claim's stored bytes are exactly what was seeded.
func requireLiveClaimUntouched(t *testing.T, path string, seed twoProcSeed) {
	t.Helper()
	d := openTestDriver(t, path)
	raw, err := d.Get(context.Background(), "ns", "msg:"+seed.live)
	requireNoErr(t, err, "read live claim after both processes")
	if !bytes.Equal(raw, seed.liveBytes) {
		t.Fatalf("unexpired claim %q was rewritten: before %x, after %x", seed.live, seed.liveBytes, raw)
	}
	requireNoErr(t, d.Close(), "close verification driver")
}

func withoutID(ids []string, drop string) []string {
	var out []string
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}
