// Purpose: the clipboard writer's ordering, refusal and hold tests over a
//
//	fake backend, plus the fakes the sibling clipboard tests share. No test
//	here touches the real clipboard or HOME.
//
// SPORT: internal/secrets ClipboardWriter/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
)

var clipNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// trace is the one ordered record every fake appends to.
type trace struct {
	mu    sync.Mutex
	steps []string
}

func (t *trace) add(s string) { t.mu.Lock(); t.steps = append(t.steps, s); t.mu.Unlock() }
func (t *trace) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.steps)
}
func (t *trace) count(s string) int {
	n := 0
	for _, x := range t.list() {
		if x == s {
			n++
		}
	}
	return n
}

type fakeOps struct {
	tr                         *trace
	probeErr, setErr, clearErr error
	mu                         sync.Mutex
	payloads                   [][]byte
}

func (f *fakeOps) platform() string { return "fake" }
func (f *fakeOps) probe() error     { f.tr.add("probe"); return f.probeErr }
func (f *fakeOps) setValue(_ context.Context, p []byte) error {
	f.tr.add("set")
	f.mu.Lock()
	f.payloads = append(f.payloads, slices.Clone(p))
	f.mu.Unlock()
	return f.setErr
}
func (f *fakeOps) clearValue(context.Context) error { f.tr.add("clear"); return f.clearErr }

// fakeEffects is an in-memory effect log that also lists open intents.
type fakeEffects struct {
	tr                   *trace
	beginErr, confirmErr error
	pendingErr           error  // PendingEffects fails
	unknownErr           error  // MarkUnknownOutcome fails
	failBegin            string // BeginEffect fails for keys with this prefix
	failConfirm          string // ConfirmEffect fails for keys with this prefix
	mu                   sync.Mutex
	seq                  uint64
	phase                map[string]audit.EffectPhase
	order                []string
	explains             []string
}

func newFakeEffects(tr *trace) *fakeEffects {
	return &fakeEffects{tr: tr, phase: map[string]audit.EffectPhase{}}
}

func actionOf(key string) string { return strings.SplitN(key, ":", 2)[0] }

func (f *fakeEffects) BeginEffect(_ context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	f.tr.add("begin:" + actionOf(req.Key))
	if f.failBegin != "" && strings.HasPrefix(req.Key, f.failBegin) {
		return audit.EffectHandle{}, f.beginErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.phase[req.Key]; ok {
		return audit.EffectHandle{}, audit.ErrEffectExists
	}
	f.seq++
	f.phase[req.Key] = audit.EffectIntent
	f.order = append(f.order, req.Key)
	f.explains = append(f.explains, string(req.Explain)+req.Action+req.Actor)
	return audit.EffectHandle{Key: req.Key, Kind: req.Kind, IntentSeq: f.seq}, nil
}

func (f *fakeEffects) end(h audit.EffectHandle, step string, p audit.EffectPhase, err error) (audit.Record, error) {
	f.tr.add(step + ":" + actionOf(h.Key))
	if err != nil {
		return audit.Record{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.phase[h.Key] != audit.EffectIntent {
		return audit.Record{}, audit.ErrEffectNotPending
	}
	f.phase[h.Key] = p
	return audit.Record{}, nil
}

func (f *fakeEffects) ConfirmEffect(_ context.Context, h audit.EffectHandle, _ string) (audit.Record, error) {
	err := f.confirmErr
	if f.failConfirm != "" && strings.HasPrefix(h.Key, f.failConfirm) {
		err = errors.New("confirm refused")
	}
	return f.end(h, "confirm", audit.EffectConfirmed, err)
}
func (f *fakeEffects) FailEffect(_ context.Context, h audit.EffectHandle, _ string) (audit.Record, error) {
	return f.end(h, "fail", audit.EffectFailed, nil)
}
func (f *fakeEffects) MarkUnknownOutcome(_ context.Context, h audit.EffectHandle) (audit.Record, error) {
	return f.end(h, "unknown", audit.EffectUnknown, f.unknownErr)
}
func (f *fakeEffects) PendingEffects(context.Context) ([]audit.EffectHandle, error) {
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []audit.EffectHandle
	for i, k := range f.order {
		if f.phase[k] == audit.EffectIntent {
			out = append(out, audit.EffectHandle{Key: k, IntentSeq: uint64(i + 1)})
		}
	}
	return out, nil
}
func (f *fakeEffects) phaseOf(key string) audit.EffectPhase {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.phase[key]
}

type fakeAudit struct {
	err    error
	events []audit.Event
}

func (a *fakeAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	a.events = append(a.events, e)
	return audit.Record{}, a.err
}

type fakeStore struct {
	tr                      *trace
	putErr, listErr, delErr error
	mu                      sync.Mutex
	rows                    map[string]PendingClear
}

func newFakeStore(tr *trace) *fakeStore { return &fakeStore{tr: tr, rows: map[string]PendingClear{}} }
func (s *fakeStore) Put(_ context.Context, pc PendingClear) error {
	s.tr.add("put")
	if s.putErr != nil {
		return s.putErr
	}
	s.mu.Lock()
	s.rows[pc.RefID] = pc
	s.mu.Unlock()
	return nil
}
func (s *fakeStore) List(context.Context) ([]PendingClear, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingClear, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}
func (s *fakeStore) Delete(_ context.Context, ref string) error {
	s.tr.add("delete")
	if s.delErr != nil {
		return s.delErr
	}
	s.mu.Lock()
	delete(s.rows, ref)
	s.mu.Unlock()
	return nil
}
func (s *fakeStore) len() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.rows) }

// fakeScheduler records each arm. With auto it runs fn at once (the TTL
// elapsing); otherwise fire runs it. armed reports every arm; onStop, when
// set, receives every stop.
type fakeScheduler struct {
	auto    bool
	armed   chan time.Duration
	onStop  chan struct{}
	mu      sync.Mutex
	fns     []func()
	delays  []time.Duration
	stopped int
}

func (s *fakeScheduler) AfterFunc(d time.Duration, fn func()) func() {
	s.mu.Lock()
	s.fns, s.delays = append(s.fns, fn), append(s.delays, d)
	s.mu.Unlock()
	if s.armed != nil {
		s.armed <- d
	}
	if s.auto {
		fn()
	}
	return func() {
		s.mu.Lock()
		s.stopped++
		s.mu.Unlock()
		if s.onStop != nil {
			s.onStop <- struct{}{}
		}
	}
}
func (s *fakeScheduler) fire(i int) { s.mu.Lock(); fn := s.fns[i]; s.mu.Unlock(); fn() }

type fakeVerifier struct{ verb string }

func (v fakeVerifier) Verify([]byte) (*policy.ApprovalRecord, error) {
	return &policy.ApprovalRecord{Verb: v.verb}, nil
}

type rig struct {
	tr    *trace
	ops   *fakeOps
	fx    *fakeEffects
	aw    *fakeAudit
	store *fakeStore
	sched *fakeScheduler
}

func newRig() *rig {
	tr := &trace{}
	return &rig{tr: tr, ops: &fakeOps{tr: tr}, fx: newFakeEffects(tr), aw: &fakeAudit{}, store: newFakeStore(tr),
		sched: &fakeScheduler{auto: true}}
}

func (r *rig) writer(t *testing.T, v ApprovalVerifier) ClipboardWriter {
	t.Helper()
	if v == nil {
		v = fakeVerifier{verb: ClipboardApprovalVerb}
	}
	w, err := newClipboardWriterWithOps(runtime.NewFixedClock(clipNow), r.aw, r.fx, v, r.store, r.sched, r.ops, 30*time.Second)
	if err != nil {
		t.Fatalf("newClipboardWriterWithOps: %v", err)
	}
	return w
}

// TestClipboardWriteAudited: intent, row, set, confirm, then the clear's
// intent, clear, confirm and row removal, in that order, with no payload
// byte in any record.
func TestClipboardWriteAudited(t *testing.T) {
	r := newRig()
	if err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("s3cret-value")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := []string{"probe", "begin:clipboard.write", "put", "set", "confirm:clipboard.write",
		"begin:clipboard.clear", "clear", "confirm:clipboard.clear", "delete"}
	if got := r.tr.list(); !slices.Equal(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
	for _, e := range r.fx.explains {
		if strings.Contains(e, "s3cret") {
			t.Fatalf("an effect record carries the value: %s", e)
		}
	}
	if r.store.len() != 0 || len(r.fx.explains) != 2 {
		t.Fatalf("rows = %d, records = %d; want 0 rows and 2 intents", r.store.len(), len(r.fx.explains))
	}
}
