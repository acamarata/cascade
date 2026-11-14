package delegation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// stubQueue scripts the queue answers the refusal branches need.
type stubQueue struct {
	policy.ApprovalQueue
	enqRes     policy.EnqueueResult
	enqErr     error
	pendingErr error
	pending    []policy.PendingEntry
	mu         sync.Mutex
	cancelled  []string
}

func (q *stubQueue) Enqueue(context.Context, policy.EnqueueRequest) (policy.EnqueueResult, error) {
	return q.enqRes, q.enqErr
}

func (q *stubQueue) GetPending(context.Context) ([]policy.PendingEntry, error) {
	return q.pending, q.pendingErr
}

func (q *stubQueue) Cancel(_ context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelled = append(q.cancelled, id)
	return nil
}

// stubDriver records answers; ch may be nil.
type stubDriver struct {
	provider.AgentProvider
	ch      <-chan provider.ApprovalRequest
	mu      sync.Mutex
	answers []string
}

func (d *stubDriver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.ch }

func (d *stubDriver) ResolveApproval(id, token string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers = append(d.answers, id+"="+token)
	return nil
}

func (d *stubDriver) got() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.answers...)
}

func stubBridge(q policy.ApprovalQueue, d *stubDriver, timeout time.Duration) approvalBridge {
	return approvalBridge{
		q: q, h: agents.SpawnHandle{JobID: "job-1", Driver: d},
		rec: jobs.DelegationRecord{ID: "deleg-1", AgentPrincipal: "agent-1"}, timeout: timeout,
		clock: runtime.NewSystemClock(), poll: time.Millisecond,
	}
}

func oneRequest() <-chan provider.ApprovalRequest {
	ch := make(chan provider.ApprovalRequest, 1)
	ch <- provider.ApprovalRequest{ID: "req-1", JobID: "job-1", Prompt: "run it", Kind: provider.ApprovalKindCommand}
	close(ch)
	return ch
}

func TestApprovalBridgeRefusesIncompleteSetup(t *testing.T) {
	d := &stubDriver{}
	cases := map[string]approvalBridge{
		"no queue":   stubBridge(nil, d, time.Second),
		"no timeout": stubBridge(&stubQueue{}, d, 0),
		"no driver":  {q: &stubQueue{}, timeout: time.Second, clock: runtime.NewSystemClock(), poll: time.Millisecond},
	}
	for name, b := range cases {
		err := b.run(context.Background())
		if !isKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: got %v, want an invalid-input refusal", name, err)
		}
	}
	err := BridgeApprovals(context.Background(), nil, agents.SpawnHandle{}, jobs.DelegationRecord{}, time.Second)
	if !isKind(err, cascade.KindInvalidInput) {
		t.Errorf("BridgeApprovals without a queue: got %v", err)
	}
}

func TestApprovalBridgeNilChannelReturnsNil(t *testing.T) {
	if err := stubBridge(&stubQueue{}, &stubDriver{}, time.Second).run(context.Background()); err != nil {
		t.Fatalf("a driver with no request channel must end cleanly: %v", err)
	}
}

func TestApprovalBridgeEnqueueErrorRefusesDriver(t *testing.T) {
	boom := errors.New("queue is down")
	d := &stubDriver{ch: oneRequest()}
	err := stubBridge(&stubQueue{enqErr: boom}, d, time.Second).run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("the enqueue error was not reported: %v", err)
	}
	if got := d.got(); len(got) != 1 || got[0] != "req-1=" {
		t.Fatalf("the driver must get one empty-token answer, got %v", got)
	}
}

func TestApprovalBridgeDuplicateEntryIsAConflict(t *testing.T) {
	d := &stubDriver{ch: oneRequest()}
	q := &stubQueue{enqRes: policy.EnqueueResult{RequestID: "q-1", Deduplicated: true}}
	err := stubBridge(q, d, time.Second).run(context.Background())
	if !isKind(err, cascade.KindConflict) {
		t.Fatalf("got %v, want a conflict", err)
	}
	if got := d.got(); len(got) != 1 || got[0] != "req-1=" || len(q.cancelled) != 0 {
		t.Fatalf("a duplicate must be refused without touching the other entry: %v %v", got, q.cancelled)
	}
}

func TestApprovalBridgePendingErrorRefusesAndCancels(t *testing.T) {
	d := &stubDriver{ch: oneRequest()}
	q := &stubQueue{enqRes: policy.EnqueueResult{RequestID: "q-1"}, pendingErr: errors.New("read failed")}
	if err := stubBridge(q, d, time.Minute).run(context.Background()); err != nil {
		t.Fatalf("a queue read failure is a refusal, not a bridge error: %v", err)
	}
	if got := d.got(); len(got) != 1 || got[0] != "req-1=" {
		t.Fatalf("the driver must be refused, got %v", got)
	}
	if len(q.cancelled) != 1 || q.cancelled[0] != "q-1" {
		t.Fatalf("the entry was not withdrawn: %v", q.cancelled)
	}
}

func TestApprovalBridgeContextEndRefusesPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan provider.ApprovalRequest, 1)
	ch <- provider.ApprovalRequest{ID: "req-1", Prompt: "wait"}
	d := &stubDriver{ch: ch}
	q := &stubQueue{
		enqRes:  policy.EnqueueResult{RequestID: "q-1"},
		pending: []policy.PendingEntry{{RequestID: "q-1"}},
	}
	done := make(chan error, 1)
	go func() { done <- stubBridge(q, d, time.Hour).run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if got := d.got(); len(got) != 1 || got[0] != "req-1=" {
		t.Fatalf("an unanswered request must be refused at shutdown: %v", got)
	}
	if len(q.cancelled) != 1 {
		t.Fatalf("the pending entry was not cancelled: %v", q.cancelled)
	}
}

func TestApprovalSummaryShapes(t *testing.T) {
	if got := approvalSummary(" \t\n\x7f "); got != approvalSummaryEmpty {
		t.Errorf("a blank prompt gave %q", got)
	}
	if got := approvalSummary("a\nb\x00c"); got != "a b c" {
		t.Errorf("control characters not blanked: %q", got)
	}
	got := approvalSummary(strings.Repeat("é", 40))
	if len(got) > approvalSummaryMax || !utf8.ValidString(got) {
		t.Errorf("the cut broke a rune or the bound: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got := approvalSummary(strings.Repeat("x", 63) + " tail"); got != strings.Repeat("x", 63) {
		t.Errorf("the trailing space after the cut was kept: %q", got)
	}
}

func isKind(err error, want cascade.Kind) bool {
	k, ok := cascade.KindOf(err)
	return ok && k == want
}
