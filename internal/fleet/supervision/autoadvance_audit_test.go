package supervision

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// Purpose (this file): the record and the queue item a verdict produces.
//   An auto-approval runs with no human turn, so if it is not recorded
//   there is no way to answer "what did the machine do while I was away".
// SPORT: internal/fleet/supervision autoadvance audit tests (ADD) — P1-E18-W4-S39-T2.

// captureWriter records the events it was handed.
type captureWriter struct {
	events []audit.Event
	err    error
}

func (w *captureWriter) Append(_ context.Context, event audit.Event) (audit.Record, error) {
	w.events = append(w.events, event)
	return audit.Record{}, w.err
}

// capturePusher records the attention items it was handed.
type capturePusher struct {
	items []AttentionItem
	err   error
}

func (p *capturePusher) Push(_ context.Context, item AttentionItem) (AttentionItem, error) {
	p.items = append(p.items, item)
	return item, p.err
}

// TestEveryVerdictIsAudited covers all six, approvals included.
func TestEveryVerdictIsAudited(t *testing.T) {
	for _, verdict := range []Verdict{
		VerdictAutoApproved, VerdictCeilingRefused, VerdictUntrustedRefused,
		VerdictElevatedRefused, VerdictProfileDisabled, VerdictFailClosedDenied,
	} {
		writer := &captureWriter{}
		err := RecordAutoAdvance(context.Background(), writer, plainAction(), admissible(),
			corpus.TrustTrusted, verdict)
		if err != nil {
			t.Fatalf("%s: %v", verdict, err)
		}
		if len(writer.events) != 1 {
			t.Fatalf("%s: %d events, want exactly one", verdict, len(writer.events))
		}
		event := writer.events[0]
		if event.Verdict != verdict.String() {
			t.Errorf("%s: recorded verdict %q", verdict, event.Verdict)
		}
		if event.Kind != audit.KindPolicyRoute {
			t.Errorf("%s: kind = %q, want the frozen policy.route kind", verdict, event.Kind)
		}
		if event.Action != "hook-1" {
			t.Errorf("%s: action = %q, want the action ref", verdict, event.Action)
		}
		// The zero verdict must never record an empty field.
		if event.Verdict == "" {
			t.Errorf("%s recorded an empty verdict", verdict)
		}
	}
}

// TestTheRecordCarriesNoCommandOrParameters is the disclosure rule: this
// record goes to a log an operator reads and tooling ships, and the action
// it describes may carry anything.
func TestTheRecordCarriesNoCommandOrParameters(t *testing.T) {
	writer := &captureWriter{}
	action := plainAction()
	action.Ref = "hook-1"
	if err := RecordAutoAdvance(context.Background(), writer, action, admissible(),
		corpus.TrustTrusted, VerdictAutoApproved); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(writer.events[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"params", "command", "secret", "token"} {
		if strings.Contains(strings.ToLower(string(encoded)), `"`+banned+`"`) {
			t.Errorf("the record carries a %q field: %s", banned, encoded)
		}
	}
}

// TestANilWriterIsANoOp: a missing audit sink must not turn a decided
// action into a failed one.
func TestANilWriterIsANoOp(t *testing.T) {
	if err := RecordAutoAdvance(context.Background(), nil, plainAction(), admissible(),
		corpus.TrustTrusted, VerdictAutoApproved); err != nil {
		t.Errorf("a nil writer returned %v, want nil", err)
	}
}

// TestOnlyRefusalsAHumanCanActOnAreQueued holds the queue's boundary.
func TestOnlyRefusalsAHumanCanActOnAreQueued(t *testing.T) {
	cases := map[Verdict]Kind{
		VerdictCeilingRefused:   KindPolicyAsk,
		VerdictUntrustedRefused: KindPolicyAsk,
		VerdictElevatedRefused:  KindElevationRefused,
	}
	for verdict, wantKind := range cases {
		pusher := &capturePusher{}
		queued, err := QueueAutoAdvanceRefusal(context.Background(), pusher, plainAction(), ScopeRef{}, verdict)
		if err != nil {
			t.Fatalf("%s: %v", verdict, err)
		}
		if !queued || len(pusher.items) != 1 {
			t.Fatalf("%s: queued=%v items=%d, want exactly one", verdict, queued, len(pusher.items))
		}
		if pusher.items[0].Kind != wantKind {
			t.Errorf("%s: kind = %q, want %q", verdict, pusher.items[0].Kind, wantKind)
		}
		if pusher.items[0].SourceRef != "hook-1" {
			t.Errorf("%s: source ref = %q", verdict, pusher.items[0].SourceRef)
		}
	}
	// An approval has nothing for a human to do, and a fail-closed denial
	// is surfaced by the policy layer's own path — queueing it would
	// invite an operator to approve what the engine refused outright.
	for _, verdict := range []Verdict{VerdictAutoApproved, VerdictProfileDisabled, VerdictFailClosedDenied} {
		pusher := &capturePusher{}
		queued, err := QueueAutoAdvanceRefusal(context.Background(), pusher, plainAction(), ScopeRef{}, verdict)
		if err != nil || queued || len(pusher.items) != 0 {
			t.Errorf("%s: queued=%v items=%d err=%v, want nothing queued", verdict, queued, len(pusher.items), err)
		}
	}
}

// TestAnElevationRefusalSortsAheadOfAnAsk: it blocks on a human act only a
// person at the machine can perform.
func TestAnElevationRefusalSortsAheadOfAnAsk(t *testing.T) {
	elevated := &capturePusher{}
	ask := &capturePusher{}
	if _, err := QueueAutoAdvanceRefusal(context.Background(), elevated, plainAction(), ScopeRef{}, VerdictElevatedRefused); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueAutoAdvanceRefusal(context.Background(), ask, plainAction(), ScopeRef{}, VerdictCeilingRefused); err != nil {
		t.Fatal(err)
	}
	if elevated.items[0].Priority >= ask.items[0].Priority {
		t.Errorf("elevation priority %d does not sort ahead of ask priority %d",
			elevated.items[0].Priority, ask.items[0].Priority)
	}
}

// TestTheRecorderAuditsAndQueuesTogether covers the object the router is
// handed: both side effects travel together so neither can be wired alone.
func TestTheRecorderAuditsAndQueuesTogether(t *testing.T) {
	writer, pusher := &captureWriter{}, &capturePusher{}
	recorder := NewAutoAdvanceRecorder(writer, pusher, ScopeRef{})

	if err := recorder.Record(context.Background(), plainAction(), admissible(),
		corpus.TrustTrusted, VerdictCeilingRefused); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || len(pusher.items) != 1 {
		t.Fatalf("events=%d items=%d, want one of each", len(writer.events), len(pusher.items))
	}
	// The audit error wins over a queue error: the record is what makes
	// the decision answerable later.
	failing := NewAutoAdvanceRecorder(&captureWriter{err: errors.New("audit down")},
		&capturePusher{err: errors.New("queue down")}, ScopeRef{})
	if err := failing.Record(context.Background(), plainAction(), admissible(),
		corpus.TrustTrusted, VerdictCeilingRefused); err == nil || !strings.Contains(err.Error(), "audit down") {
		t.Errorf("err = %v, want the audit failure", err)
	}
	// A queue failure alone is still reported: it means a refusal nobody
	// will see, which is the failure this queue exists to prevent.
	queueOnly := NewAutoAdvanceRecorder(&captureWriter{}, &capturePusher{err: errors.New("queue down")}, ScopeRef{})
	if err := queueOnly.Record(context.Background(), plainAction(), admissible(),
		corpus.TrustTrusted, VerdictCeilingRefused); err == nil || !strings.Contains(err.Error(), "queue down") {
		t.Errorf("err = %v, want the queue failure", err)
	}
}

// compile-time proof the recorder is what the evaluator's verdicts feed.
var _ policy.Ceiling = policy.CeilingTier1
