package telegram

// Purpose (this file): quarantine.go's tests — the R-21.105 compile-time
//   proof that QuarantineEvent carries no credential reference, exposure_id
//   uniqueness and non-resolvability, the injected-clock timestamp
//   (Art.7.3), and the exposure-id-generation-failure path still
//   publishing a record.
//
// SPORT: plugins/cascade-pa/telegram quarantine-tests/TEST (P1-E23-W5-S48-T3).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestQuarantinePayloadHasNoCredentialRef is the R-21.105 compile-time
// proof: QuarantineEvent has no field that could hold a credential
// reference, a message body or a digest. It reflects over the struct's
// DECLARED fields (name and json tag), not one populated value, so a new
// field added later trips this test even before anything populates it.
func TestQuarantinePayloadHasNoCredentialRef(t *testing.T) {
	forbidden := []string{"ref", "body", "digest", "value", "secret", "hash", "fingerprint"}
	typ := reflect.TypeOf(QuarantineEvent{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		tag := strings.ToLower(f.Tag.Get("json"))
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Fatalf("field %s: name contains forbidden substring %q", f.Name, bad)
			}
			if strings.Contains(tag, bad) {
				t.Fatalf("field %s: json tag %q contains forbidden substring %q", f.Name, tag, bad)
			}
		}
	}
	// The exact field set the ticket's payload names, in order — a field
	// this test does not know about is exactly the drift Art.2 exists to
	// catch.
	want := []string{"ExposureID", "Namespace", "Origin", "ChatKind", "At", "Severity"}
	if typ.NumField() != len(want) {
		t.Fatalf("QuarantineEvent has %d fields, want %d (%v)", typ.NumField(), len(want), want)
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Fatalf("field %d = %s, want %s", i, got, name)
		}
	}
}

// TestQuarantineEventNeverCarriesTheWitnessValueAtRuntime is T0 D3: the
// COMPILE-TIME field-shape proof above (TestQuarantinePayloadHasNoCredentialRef)
// does not check what actually ends up IN a populated event's fields. This
// JSON-encodes a real emitted event and asserts the witness secret — verbatim,
// or as a digest of itself — never appears anywhere in the encoded bytes. A
// mutation that sets outcome.Class to the raw scanned text (rather than the
// matched credential class) must turn this RED.
func TestQuarantineEventNeverCarriesTheWitnessValueAtRuntime(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	encoded, err := json.Marshal(sink.events[0])
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	wire := string(encoded)
	if strings.Contains(wire, witnessSecretText) {
		t.Fatalf("the emitted event carries the witness secret text verbatim: %s", wire)
	}
	sum := sha256.Sum256([]byte(witnessSecretText))
	if digest := hex.EncodeToString(sum[:]); strings.Contains(wire, digest) {
		t.Fatalf("the emitted event carries a digest of the witness secret: %s", wire)
	}
	if strings.Contains(wire, witnessSecretText[:10]) {
		t.Fatalf("the emitted event carries a prefix of the witness secret: %s", wire)
	}
}

// TestQuarantineExposureIDsAreUniqueAndOpaque: two refusals of the SAME
// witness content mint two different exposure_ids, and neither embeds the
// content that triggered it.
func TestQuarantineExposureIDsAreUniqueAndOpaque(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	ctx := context.Background()
	rig.module.dispatch(ctx, textUpdate(1, 111, 111, witnessSecretText))
	rig.module.dispatch(ctx, textUpdate(2, 111, 111, witnessSecretText))
	if len(sink.events) != 2 {
		t.Fatalf("events = %d, want 2", len(sink.events))
	}
	a, b := sink.events[0].ExposureID, sink.events[1].ExposureID
	if a == "" || b == "" {
		t.Fatal("an exposure_id was empty")
	}
	if a == b {
		t.Fatal("two refusals minted the same exposure_id")
	}
	if strings.Contains(a, witnessSecretText) || strings.Contains(b, witnessSecretText) {
		t.Fatal("an exposure_id embeds the witness secret text")
	}
}

// TestQuarantineTimestampSurvivesANilPairer is T0 D8: now() reads the
// module's OWN clock, never m.pairer.clock, so a nil-pairer module (this
// package's own dispatchUnbound is the only place a real caller ever
// touches m.pairer — refuseInboundText/publishQuarantine never did) does
// not panic minting a quarantine timestamp.
func TestQuarantineTimestampSurvivesANilPairer(t *testing.T) {
	client := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	module := NewTelegramModule(testSubject, client, nil, nil, nodeVerbPolicy(), fixedTestClock{t0()})
	module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	module.quarantine = sink
	module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	if len(sink.events) != 1 || !sink.events[0].At.Equal(t0()) {
		t.Fatalf("events = %+v, want 1 event timestamped at the injected clock", sink.events)
	}
}

// TestQuarantineTimestampUsesInjectedClock: At comes from the module's
// injected clock (fixedTestClock{t0()} via newDefaultRig), never the wall
// clock (Art.7.3) — a bare time.Now() call would make this flaky or,
// worse, silently drift from what a test asserts.
func TestQuarantineTimestampUsesInjectedClock(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	if !sink.events[0].At.Equal(t0()) {
		t.Fatalf("At = %v, want the injected clock's %v", sink.events[0].At, t0())
	}
}

// TestQuarantinePublishesEvenWhenExposureIDGenerationFails: a CSPRNG read
// failure still produces a record — an unminted exposure_id is a worse-
// but-recorded audit line, never a silently dropped refusal.
func TestQuarantinePublishesEvenWhenExposureIDGenerationFails(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	sink := &recordingQuarantineSink{}
	rig.module.quarantine = sink

	orig := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("test: csprng unavailable") }
	defer func() { randRead = orig }()

	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1 even when exposure_id generation fails", len(sink.events))
	}
	if sink.events[0].ExposureID != "" {
		t.Fatalf("ExposureID = %q, want empty when generation failed", sink.events[0].ExposureID)
	}
}

// TestQuarantineDiscardedWithNoSinkConfigured: an unconfigured sink (nil,
// T0 D1) does not panic and does not change the refusal decision itself —
// the message is still refused even though nothing records it.
// TestPublishQuarantineReportsErrNoQuarantineSink below proves the record
// loss is now typed rather than silent.
func TestQuarantineDiscardedWithNoSinkConfigured(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	called := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, witnessSecretText))
	if called {
		t.Fatal("a handler ran for a secret-shaped message with no quarantine sink configured")
	}
	if got := lastSent(t, rig.doer); got != replyBridgeSecretRefused {
		t.Fatalf("reply = %q, want the refusal", got)
	}
}

// TestPublishQuarantineReportsErrNoQuarantineSink is T0 D1's "refuse, never
// silently discard" applied to the sink, mirroring ErrNoSecretScanner's
// pattern for the scanner: with no QuarantineSink wired, publishQuarantine
// reports ErrNoQuarantineSink BY IDENTITY (not errors.Is, which on this
// tree's *cascade.Error compares Kind only and would false-match any other
// KindUnavailable value) instead of silently doing nothing — naming
// exactly what went unrecorded, never touching the refusal decision
// itself, which TestQuarantineDiscardedWithNoSinkConfigured above already
// proves is unaffected.
func TestPublishQuarantineReportsErrNoQuarantineSink(t *testing.T) {
	client := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	module := NewTelegramModule(testSubject, client, nil, nil, nodeVerbPolicy(), fixedTestClock{t0()})
	// module.quarantine left nil: deliberately unconfigured.
	err := module.publishQuarantine(context.Background(), quarantineOriginInboundText, "private",
		SecretScanOutcome{Detected: true, Class: "api-key"})
	if err != ErrNoQuarantineSink {
		t.Fatalf("publishQuarantine err = %v, want ErrNoQuarantineSink by identity", err)
	}
}

// TestPublishQuarantineRecordsWithARealSink is the counterpart: a wired
// sink still receives the event, unchanged, and publishQuarantine reports
// no error.
func TestPublishQuarantineRecordsWithARealSink(t *testing.T) {
	client := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	module := NewTelegramModule(testSubject, client, nil, nil, nodeVerbPolicy(), fixedTestClock{t0()})
	sink := &recordingQuarantineSink{}
	module.quarantine = sink
	err := module.publishQuarantine(context.Background(), quarantineOriginInboundText, "private",
		SecretScanOutcome{Detected: true, Class: "api-key"})
	if err != nil {
		t.Fatalf("publishQuarantine err = %v, want nil with a real sink wired", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
}
