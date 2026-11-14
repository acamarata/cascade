package executive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// scriptedRung answers every Try with a fixed result and counts calls.
type scriptedRung struct {
	by    string
	ok    bool
	err   error
	calls int
}

func (r *scriptedRung) Try(_ context.Context, _ Escalation) (string, bool, error) {
	r.calls++
	return r.by, r.ok, r.err
}

func newTestBus() *events.Bus {
	return events.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)))
}

type ladderRun struct {
	out    Outcome
	err    error
	events StreamSeq
	repo   *scriptedRung
	inv    *scriptedRung
}

func runLadder(ctx context.Context, t *testing.T, repo, inv *scriptedRung, e Escalation) ladderRun {
	t.Helper()
	bus := newTestBus()
	l, err := NewLadder(bus, newTestDetector(t), repo, inv)
	if err != nil {
		t.Fatalf("NewLadder: %v", err)
	}
	out, rerr := l.Resolve(ctx, e)
	var n StreamSeq
	if e.FromJob != "" {
		if n, err = StreamHead(ctx, bus, StreamID(e.FromJob)); err != nil {
			t.Fatalf("StreamHead: %v", err)
		}
	}
	return ladderRun{out: out, err: rerr, events: n, repo: repo, inv: inv}
}

func esc(kind EscalationKind) Escalation {
	return Escalation{FromJob: "job-1", Kind: kind, Summary: "which schema?", Options: []string{"a", "b"}}
}

// TestEscalationLadderOrder verifies the rung order, the owner route and the journal count.
func TestEscalationLadderOrder(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	r := runLadder(ctx, t, &scriptedRung{by: "repo", ok: true}, &scriptedRung{by: "inv", ok: true}, esc(EscalationAmbiguity))
	if r.err != nil || r.out.Resolution != ResolutionRepoContext || r.out.ResolvedBy != "repo" || r.inv.calls != 0 || r.events != 1 {
		t.Fatalf("repo ok: %+v err %v inv calls %d events %d", r.out, r.err, r.inv.calls, r.events)
	}
	r = runLadder(ctx, t, &scriptedRung{}, &scriptedRung{by: "inv", ok: true}, esc(EscalationConflict))
	if r.err != nil || r.out.Resolution != ResolutionInvestigator || r.out.ResolvedBy != "inv" || r.events != 2 {
		t.Fatalf("investigator ok: %+v err %v events %d", r.out, r.err, r.events)
	}
	r = runLadder(ctx, t, &scriptedRung{ok: true}, &scriptedRung{ok: true}, esc(EscalationAuthority))
	if r.err != nil || r.out.Resolution != ResolutionOwner || r.repo.calls+r.inv.calls != 0 {
		t.Fatalf("authority: %+v err %v calls %d/%d", r.out, r.err, r.repo.calls, r.inv.calls)
	}
	r = runLadder(ctx, t, &scriptedRung{}, &scriptedRung{}, esc(EscalationMissingInfo))
	if r.err != nil || r.out.Resolution != ResolutionRefused || !r.out.Blocked || r.events != 3 {
		t.Fatalf("missing_info refused: %+v err %v events %d", r.out, r.err, r.events)
	}
	boom := errors.New("rung down")
	r = runLadder(ctx, t, &scriptedRung{err: boom}, &scriptedRung{ok: true}, esc(EscalationArchitectural))
	if !cascade.HasKind(r.err, cascade.KindUnavailable) || !errors.Is(r.err, boom) || r.inv.calls != 0 {
		t.Fatalf("rung error: err %v inv calls %d", r.err, r.inv.calls)
	}
	if _, err := NewLadder(newTestBus(), newTestDetector(t), nil, &scriptedRung{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil rung: %v", err)
	}
}

// TestWorkerNeverAddressesOwner verifies only authority reaches the owner and bad input journals nothing.
func TestWorkerNeverAddressesOwner(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	for _, k := range []EscalationKind{EscalationAmbiguity, EscalationConflict, EscalationArchitectural, EscalationMissingInfo} {
		r := runLadder(ctx, t, &scriptedRung{}, &scriptedRung{}, esc(k))
		if r.err != nil || r.out.Resolution != ResolutionRefused {
			t.Errorf("%s: %+v err %v; want refused", k, r.out, r.err)
		}
		if r.out.Blocked != (k == EscalationMissingInfo) {
			t.Errorf("%s: Blocked %v", k, r.out.Blocked)
		}
	}
	typ := reflect.TypeOf(Escalation{})
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
	}
	if strings.Join(names, ",") != "FromJob,Kind,Summary,Options" {
		t.Fatalf("Escalation fields = %v", names)
	}
	bad := []Escalation{
		{Kind: EscalationConflict, Summary: "s"},
		{FromJob: "job-1", Kind: EscalationConflict},
		{FromJob: "job-1", Kind: "urgent", Summary: "s"},
		{FromJob: "job-1", Kind: EscalationConflict, Summary: "key " + skCanary()},
	}
	for _, e := range bad {
		r := runLadder(ctx, t, &scriptedRung{ok: true}, &scriptedRung{ok: true}, e)
		if !cascade.HasKind(r.err, cascade.KindInvalidInput) || r.events != 0 || r.repo.calls != 0 {
			t.Errorf("%+v: err %v events %d repo calls %d", e, r.err, r.events, r.repo.calls)
		}
	}
}

// TestEscalationKindRegistered verifies exec.ESCALATION is in the kind, payload and producer tables.
func TestEscalationKindRegistered(t *testing.T) {
	isolateHome(t)
	if len(AllKinds) != 42 || AllKinds[len(AllKinds)-1] != KindEscalation || KindEscalation != "exec.ESCALATION" {
		t.Fatalf("AllKinds has %d, last %q", len(AllKinds), AllKinds[len(AllKinds)-1])
	}
	if g, err := GroupOf(KindEscalation); err != nil || g != GroupEscalation || GroupEscalation != "escalation" {
		t.Fatalf("GroupOf = %q, %v", g, err)
	}
	ok := `{"from_job":"j","kind":"ambiguity","summary":"s","step":"repo_context","outcome":"resolved"}`
	if err := ValidatePayload(KindEscalation, PayloadV1, []byte(ok)); err != nil {
		t.Fatalf("ValidatePayload: %v", err)
	}
	for _, name := range []string{"exec-kinds.golden.json", "payload-schema.golden.json", "adapter-table.golden.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(raw), `"exec.ESCALATION"`) {
			t.Errorf("%s does not list exec.ESCALATION", name)
		}
	}
	found := false
	for _, p := range ProducerRows() {
		if p.Exec == KindEscalation {
			found = p.Live && p.Producer == escalationProducer
		}
	}
	if !found {
		t.Fatal("ProducerRows lacks a live exec.ESCALATION row")
	}
}
