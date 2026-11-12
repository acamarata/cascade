package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd"
)

type pbdEvidenceDouble struct {
	t       *testing.T
	listing string
	events  []client.Event
	err     error
	closed  bool
}

func (d *pbdEvidenceDouble) Do(_ context.Context, method string, params, out any) error {
	d.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil || method != "jobs.evidence.list" || string(raw) != `{"ticket":"P1-X"}` {
		d.t.Fatalf("RPC request: %s %s (%v)", method, raw, err)
	}
	if d.err != nil {
		return d.err
	}
	return json.Unmarshal([]byte(d.listing), out)
}

func (d *pbdEvidenceDouble) StreamTopic(_ context.Context, topic string, query url.Values) (<-chan client.Event, func(), error) {
	d.t.Helper()
	if topic != "jobs.evidence" || !reflect.DeepEqual(query, url.Values{"ticket": {"P1-X"}}) {
		d.t.Fatalf("subscription: %s %v", topic, query)
	}
	if d.err != nil {
		return nil, nil, d.err
	}
	events := make(chan client.Event, len(d.events))
	for _, event := range d.events {
		events <- event
	}
	close(events)
	return events, func() { d.closed = true }, nil
}

func pbdEvidenceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	return home
}

const pbdEvidenceRow = `{"job_id":"P1-X","seq":7,"kind":"test","producer_capability":"go.test","attempt_id":"attempt","checkpoint_id":"checkpoint","tree_hash":"tree","attestor_identity":"attestor","outcome":"pass","result_hash":"result","idempotency_key":"key","artifact_ref":"artifact","started_at":"2026-11-01T01:00:00Z","ended_at":"2026-11-01T01:01:00Z","created_at":"2026-11-01T01:02:00Z","data_class":"internal","redacted":false}`
const pbdEvidenceRedacted = `{"job_id":"P1-X","seq":8,"kind":"test","outcome":"pass","data_class":"secret","redacted":true}`

func TestPBDEvidenceWiringMapsRows(t *testing.T) {
	pbdEvidenceHome(t)
	start := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	want := []pbd.EvidenceEntry{{JobID: "P1-X", Seq: 7, Kind: "test", ProducerCapability: "go.test",
		AttemptID: "attempt", CheckpointID: "checkpoint", TreeHash: "tree", AttestorIdentity: "attestor",
		Outcome: "pass", ResultHash: "result", IdempotencyKey: "key", ArtifactRef: "artifact",
		StartedAt: start, EndedAt: start.Add(time.Minute), CreatedAt: start.Add(2 * time.Minute), DataClass: "internal"},
		{JobID: "P1-X", Seq: 8, Kind: "test", Outcome: "pass", DataClass: "secret", Redacted: true}}
	d := &pbdEvidenceDouble{t: t, listing: `{"ticket":"P1-X","rows":[` + pbdEvidenceRow + `,` + pbdEvidenceRedacted + `]}`}
	r := &pbdEvidenceReader{doer: d}
	rows, err := r.QueryAll(context.Background(), "P1-X")
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("listing = %+v, %v; want %+v", rows, err, want)
	}
	d.events = []client.Event{{Data: pbdEvidenceRow}, {Data: pbdEvidenceRedacted}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var emitted []pbd.EvidenceEntry
	err = r.Watch(ctx, "P1-X", func(row pbd.EvidenceEntry) error {
		emitted = append(emitted, row)
		if len(emitted) == len(want) {
			cancel()
		}
		return nil
	})
	if err != nil || !d.closed || !reflect.DeepEqual(emitted, want) {
		t.Fatalf("watch = %+v, %v; closed=%v", emitted, err, d.closed)
	}
	d.listing = `{"ticket":"P1-X","rows":[]}`
	rows, err = r.QueryAll(context.Background(), "P1-X")
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty listing = %+v, %v", rows, err)
	}
}

func TestPBDEvidenceWiringPropagatesErrors(t *testing.T) {
	home := pbdEvidenceHome(t)
	r := newPBDEvidenceReader(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: filepath.Join(home, "missing.sock")}, nil
	})
	rows, err := r.QueryAll(context.Background(), "P1-X")
	if rows != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("dial failure = %+v, %v", rows, err)
	}
	err = r.Watch(context.Background(), "P1-X", func(pbd.EvidenceEntry) error { t.Fatal("emitted after dial failure"); return nil })
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("watch dial failure = %v", err)
	}
	t.Run("permission", pbdEvidencePermission)
	t.Run("stream failures", pbdEvidenceStreamFailures)
	t.Run("identity transport", func(t *testing.T) { pbdEvidenceIdentityTransport(t, r) })
}

func pbdEvidencePermission(t *testing.T) {
	want := cascade.New(cascade.KindPermissionDenied, "evidence owner only")
	d := &pbdEvidenceDouble{t: t, err: want}
	r := &pbdEvidenceReader{doer: d}
	rows, err := r.QueryAll(context.Background(), "P1-X")
	if err != want || err.Error() != "permission-denied: evidence owner only" || rows != nil {
		t.Fatalf("RPC refusal = %+v, %v", rows, err)
	}
	if err := r.Watch(context.Background(), "P1-X", nil); err != want || err.Error() != "permission-denied: evidence owner only" {
		t.Fatalf("stream refusal = %v", err)
	}
	d.err, d.listing = nil, `{"ticket":"P1-X","rows":[`+pbdEvidenceRow+`]}`
	if rows, err := r.QueryAll(context.Background(), "P1-X"); err != nil || len(rows) != 1 {
		t.Fatalf("positive listing = %+v, %v", rows, err)
	}
	d.listing = `{"rows":[{"seq":"invalid"}]}`
	if rows, err := r.QueryAll(context.Background(), "P1-X"); err == nil || rows != nil {
		t.Fatalf("malformed listing = %+v, %v", rows, err)
	}
}

func pbdEvidenceStreamFailures(t *testing.T) {
	want := errors.New("output stopped")
	d := &pbdEvidenceDouble{t: t, events: []client.Event{{Data: pbdEvidenceRow}}}
	r := &pbdEvidenceReader{doer: d}
	err := r.Watch(context.Background(), "P1-X", func(pbd.EvidenceEntry) error { return want })
	if err != want || err.Error() != "output stopped" || !d.closed {
		t.Fatalf("emit failure = %v; closed=%v", err, d.closed)
	}
	d.events = []client.Event{{Err: want}}
	if err := r.Watch(context.Background(), "P1-X", nil); err != want || err.Error() != "output stopped" {
		t.Fatalf("event failure = %v", err)
	}
	d.events = []client.Event{{Data: `{"seq":"invalid"}`}}
	if err := r.Watch(context.Background(), "P1-X", nil); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("bad row = %v", err)
	}
	d.events = nil
	if err := r.Watch(context.Background(), "P1-X", nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("closed stream = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Watch(ctx, "P1-X", nil); err != nil {
		t.Fatalf("canceled watch = %v", err)
	}
}

// The SDK owns header behavior. This unit check guards use of its dynamic
// principal transport, rather than a token bound at reader construction.
func pbdEvidenceIdentityTransport(t *testing.T, r *pbdEvidenceReader) {
	t.Helper()
	rpc, err := r.rpcClient()
	if err != nil {
		t.Fatal(err)
	}
	c, ok := rpc.(*client.Client)
	if !ok {
		t.Fatalf("production RPC client = %T", rpc)
	}
	for _, token := range []string{"", "unit-first", "unit-second"} {
		t.Setenv(principal.EnvToken, token)
		if token == "" {
			if err := os.Unsetenv(principal.EnvToken); err != nil {
				t.Fatal(err)
			}
		}
		transport := reflect.ValueOf(c).Elem().FieldByName("httpClient").Elem().FieldByName("Transport").Elem()
		if transport.Type().String() != "*client.principalTransport" {
			t.Fatalf("production transport = %s; want per-request principal transport", transport.Type())
		}
		// Reuse this client across identity changes, exercising both SDK paths.
		reader := &pbdEvidenceReader{doer: c}
		if _, err := reader.QueryAll(context.Background(), "P1-X"); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("RPC transport classification = %v", err)
		}
		if err := reader.Watch(context.Background(), "P1-X", nil); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("stream transport classification = %v", err)
		}
	}
}
