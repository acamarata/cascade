package executive

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func deliveryEnv(id string, seq StreamSeq, producer Producer) *Envelope {
	return &Envelope{
		EventID: id, Kind: KindTaskStarted, StreamID: "run-1", StreamSeq: seq,
		RunID: "run-1", Producer: producer, PayloadVersion: 1,
		Payload: Payload(`{}`), OccurredAt: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC),
	}
}

func newTestDeliverer(t *testing.T, window int) *Deliverer {
	t.Helper()
	isolateHome(t)
	d, err := NewDeliverer(window)
	if err != nil {
		t.Fatalf("NewDeliverer(%d): %v", window, err)
	}
	return d
}

func mustDeliver(t *testing.T, d *Deliverer, env *Envelope, want bool) {
	t.Helper()
	got, err := d.Deliver(env)
	if err != nil || got != want {
		t.Fatalf("Deliver(%s seq %d %s) = %v, %v; want %v, nil", env.EventID, env.StreamSeq, env.Producer, got, err, want)
	}
}

// TestEXE01LegacyAC7 verifies an envelope is dropped only on an exact repeat
// or a retried emission of an already-delivered EventID.
func TestEXE01LegacyAC7(t *testing.T) {
	d := newTestDeliverer(t, DefaultDedupeWindow)
	delivered := map[string]bool{}

	e1 := deliveryEnv("e1", 1, "sess-a")
	raw, err := json.Marshal(e1)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i, want := range []bool{true, false} {
		env, ok, err := d.Accept(raw)
		if err != nil || ok != want {
			t.Fatalf("Accept #%d = %v, %v; want %v", i, ok, err, want)
		}
		if ok {
			delivered[env.EventID] = true
		}
	}
	if d.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", d.Dropped())
	}
	mustDeliver(t, d, deliveryEnv("e2", 2, "sess-b"), true)
	delivered["e2"] = true

	mustDeliver(t, d, deliveryEnv("e1", 3, "sess-a"), false)
	if d.Dropped() != 2 || d.HighWater("run-1") != 3 {
		t.Fatalf("after retry: Dropped %d HighWater %d; want 2, 3", d.Dropped(), d.HighWater("run-1"))
	}
	mustDeliver(t, d, deliveryEnv("e4", 4, "sess-a"), true)
	delivered["e4"] = true
	if len(delivered) != 3 {
		t.Fatalf("delivered %d distinct events, want 3", len(delivered))
	}
}

// TestDeliverRefusesSequenceConflict verifies contradicting sequences and
// invalid arguments are refused and nothing is recorded.
func TestDeliverRefusesSequenceConflict(t *testing.T) {
	d := newTestDeliverer(t, DefaultDedupeWindow)
	mustDeliver(t, d, deliveryEnv("e1", 1, "sess-a"), true)
	mustDeliver(t, d, deliveryEnv("e2", 2, "sess-a"), true)
	if _, err := d.Deliver(deliveryEnv("e2b", 2, "sess-b")); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("seq 2 under another producer: err %v, want ErrSequenceConflict", err)
	}
	if err := d.Restore("run-1", 5); err != nil {
		t.Fatalf("Restore(5): %v", err)
	}
	if _, err := d.Deliver(deliveryEnv("e3", 3, "sess-a")); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("seq 3 after restore: err %v, want ErrSequenceConflict", err)
	}
	mustDeliver(t, d, deliveryEnv("e6", 6, "sess-a"), true)
	if err := d.Restore("run-1", 4); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("Restore below high-water: err %v", err)
	}
	if d.HighWater("run-1") != 6 || d.Dropped() != 0 {
		t.Fatalf("HighWater %d Dropped %d; want 6, 0", d.HighWater("run-1"), d.Dropped())
	}
	if _, err := NewDeliverer(0); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("NewDeliverer(0): err %v", err)
	}
	if _, err := d.Deliver(deliveryEnv("e7", 7, "")); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("empty producer: err %v", err)
	}
}

// TestDeliverRefusesInvalidEnvelope covers every unkeyable envelope.
func TestDeliverRefusesInvalidEnvelope(t *testing.T) {
	d := newTestDeliverer(t, 4)
	noID := deliveryEnv("", 1, "sess-a")
	noStream := deliveryEnv("e1", 1, "sess-a")
	noStream.StreamID = ""
	for name, env := range map[string]*Envelope{
		"nil": nil, "empty event id": noID, "empty stream": noStream,
		"seq 0": deliveryEnv("e1", 0, "sess-a"),
	} {
		if _, err := d.Deliver(env); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("%s: err %v, want ErrInvalidDelivery", name, err)
		}
	}
	if err := d.Restore("", 1); !errors.Is(err, ErrInvalidDelivery) {
		t.Errorf("Restore empty stream: err %v", err)
	}
	if env, ok, err := d.Accept([]byte(`{bad`)); env != nil || ok || !errors.Is(err, errMalformed) {
		t.Errorf("Accept malformed = %v, %v, %v", env, ok, err)
	}
	if d.HighWater("other") != 0 {
		t.Errorf("unknown stream high-water = %d", d.HighWater("other"))
	}
}

// TestDeliverWindowEvictsOldest verifies only the newest window keys and
// EventIDs stay recorded per stream.
func TestDeliverWindowEvictsOldest(t *testing.T) {
	d := newTestDeliverer(t, 2)
	for seq := StreamSeq(1); seq <= 3; seq++ {
		mustDeliver(t, d, deliveryEnv("e"+string(rune('0'+seq)), seq, "sess-a"), true)
	}
	mustDeliver(t, d, deliveryEnv("e3", 3, "sess-a"), false)
	if _, err := d.Deliver(deliveryEnv("e1", 1, "sess-a")); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("evicted seq 1: err %v, want ErrSequenceConflict", err)
	}
	if d.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", d.Dropped())
	}
}

// TestAcceptDecodePaths verifies Accept's strict decode path: nested
// duplicate keys and broken nested JSON refuse, nested payloads and a nil
// payload round-trip.
func TestAcceptDecodePaths(t *testing.T) {
	d := newTestDeliverer(t, 4)
	head := `{"event_id":"e1","kind":"exec.TASK_STARTED","stream_id":"run-1","stream_seq":1,"producer":"sess-a","payload":`
	refused := map[string]string{
		"nested duplicate":       head + `{"a":[{"k":1,"k":2}]}}`,
		"broken nested object":   head + `{"a":}}`,
		"unterminated object":    `{"event_id":"e1"`,
		"broken key in object":   `{"event_id":"e1",1:2}`,
		"duplicate in array obj": head + `[{"x":1},{"x":1,"x":2}]}`,
	}
	for name, raw := range refused {
		if _, ok, err := d.Accept([]byte(raw)); ok || err == nil {
			t.Errorf("%s: Accept = %v, %v; want refusal", name, ok, err)
		}
	}
	env, ok, err := d.Accept([]byte(head + `{"a":[{"b":[1,2]}],"c":{}}}`))
	if err != nil || !ok || string(env.Payload) != `{"a":[{"b":[1,2]}],"c":{}}` {
		t.Fatalf("nested payload: env %+v ok %v err %v", env, ok, err)
	}
	if _, _, err := d.Accept(nil); err == nil {
		t.Fatal("Accept(nil) succeeded")
	}
	noPayload := deliveryEnv("e2", 2, "sess-a")
	noPayload.Payload = nil
	raw, err := json.Marshal(noPayload)
	if err != nil {
		t.Fatalf("marshal nil payload: %v", err)
	}
	if _, ok, err := d.Accept(raw); err != nil || !ok {
		t.Fatalf("nil payload: ok %v err %v", ok, err)
	}
	if err := (*Payload)(nil).UnmarshalJSON([]byte(`{}`)); err == nil {
		t.Fatal("UnmarshalJSON on nil Payload succeeded")
	}
}
