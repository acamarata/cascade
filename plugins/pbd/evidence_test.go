package pbd

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type evidenceTestReader struct {
	rows, emitted []EvidenceEntry
	err, watchErr error
	cancel        context.CancelFunc
}

// QueryAll supplies the test ledger and rejects accidental empty-ticket calls.
func (r evidenceTestReader) QueryAll(_ context.Context, ticket string) ([]EvidenceEntry, error) {
	if ticket == "" {
		panic("empty ticket reached reader")
	}
	return r.rows, r.err
}

// Watch emits deterministic test rows before cancellation or a configured error.
func (r evidenceTestReader) Watch(ctx context.Context, ticket string, emit func(EvidenceEntry) error) error {
	if ticket == "" {
		panic("empty ticket reached reader")
	}
	for _, row := range r.emitted {
		if err := emit(row); err != nil {
			return err
		}
	}
	if r.cancel != nil {
		r.cancel()
		return ctx.Err()
	}
	return r.watchErr
}

func installEvidenceTestReader(t *testing.T, r EvidenceReader) {
	t.Helper()
	prior := snapshotEvidenceReader()
	t.Cleanup(func() { _ = SetEvidenceReader(prior) })
	if err := SetEvidenceReader(r); err != nil {
		t.Fatal(err)
	}
}

func evidenceTestRows() []EvidenceEntry {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := EvidenceEntry{JobID: "T-1", Seq: 2, Kind: "test", Outcome: "passed",
		ProducerCapability: "verify", AttemptID: "attempt", CheckpointID: "checkpoint",
		TreeHash: "tree", AttestorIdentity: "attestor", ResultHash: "result",
		IdempotencyKey: "key", ArtifactRef: "artifact-2", StartedAt: now,
		EndedAt: now, CreatedAt: now, DataClass: "public"}
	first := row
	first.Seq, first.ArtifactRef, first.DataClass = 1, "artifact-1", "local_only"
	return []EvidenceEntry{row, first}
}

// TestPBDEvidenceCommand proves table ordering, wire keys, and input validation.
func TestPBDEvidenceCommand(t *testing.T) {
	installEvidenceTestReader(t, evidenceTestReader{rows: evidenceTestRows()})
	var out, stderr bytes.Buffer
	if err := RunEvidence(context.Background(), []string{"T-1"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "SEQ KIND OUTCOME DATA_CLASS PRODUCER ARTIFACT_REF\n" +
		"1 test passed local_only verify artifact-1\n2 test passed public verify artifact-2\n"
	if out.String() != want || stderr.Len() != 0 {
		t.Fatalf("table = %q, stderr = %q", out.String(), stderr.String())
	}
	out.Reset()
	if err := RunEvidence(context.Background(), []string{"--json", "T-1"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	assertEvidenceJSONKeys(t, out.Bytes())
	if err := RunEvidence(context.Background(), nil, &out, &stderr); err != ErrEvidenceTicketRequired {
		t.Fatalf("missing ticket = %v", err)
	}
	for _, args := range [][]string{{"--bogus", "T-1"}, {"T-1", "T-2"}, {""}} {
		if err := RunEvidence(context.Background(), args, &out, &stderr); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("args %v: %v", args, err)
		}
	}
	for _, raw := range []string{"{", `{}`, `{"ticket":""}`, `null`, `{"ticket":2}`} {
		if _, err := EvidenceRPC(context.Background(), json.RawMessage(raw)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("params %q: %v", raw, err)
		}
	}
}

func assertEvidenceJSONKeys(t *testing.T, data []byte) {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	keys := strings.Fields("job_id seq kind producer_capability attempt_id checkpoint_id tree_hash attestor_identity outcome result_hash idempotency_key artifact_ref started_at ended_at created_at data_class redacted")
	if len(rows) != 2 || rows[0]["seq"] != float64(1) || rows[1]["seq"] != float64(2) {
		t.Fatalf("rows = %v", rows)
	}
	for _, row := range rows {
		if len(row) != len(keys) {
			t.Fatalf("keys = %v", row)
		}
		for _, key := range keys {
			if _, ok := row[key]; !ok {
				t.Errorf("missing %s", key)
			}
		}
	}
}

// TestPBDEvidenceEmptyLedger proves an empty ledger succeeds in both formats.
func TestPBDEvidenceEmptyLedger(t *testing.T) {
	installEvidenceTestReader(t, evidenceTestReader{})
	for _, asJSON := range []bool{false, true} {
		args, want := []string{"T-1"}, ""
		if asJSON {
			args, want = append(args, "--json"), "[]\n"
		}
		var out, stderr bytes.Buffer
		if err := RunEvidence(context.Background(), args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if out.String() != want || stderr.String() != "no evidence recorded for T-1\n" {
			t.Fatalf("stdout = %q, stderr = %q", out.String(), stderr.String())
		}
	}
	got, err := EvidenceRPC(context.Background(), json.RawMessage(`{"ticket":"T-1"}`))
	if err != nil || !reflect.DeepEqual(got, []EvidenceEntry{}) {
		t.Fatalf("RPC = %#v, %v", got, err)
	}
}

// TestPBDEvidenceReaderErrorKeepsKind proves reader error identity is preserved.
func TestPBDEvidenceReaderErrorKeepsKind(t *testing.T) {
	down := cascade.New(cascade.KindUnavailable, "down")
	installEvidenceTestReader(t, evidenceTestReader{err: down})
	var out, stderr bytes.Buffer
	err := RunEvidence(context.Background(), []string{"T-1", "--json"}, &out, &stderr)
	if err != down || !cascade.HasKind(err, cascade.KindUnavailable) || out.Len() != 0 {
		t.Fatalf("command = %v, stdout = %q", err, out.String())
	}
	if _, err = EvidenceRPC(context.Background(), json.RawMessage(`{"ticket":"T-1"}`)); err != down || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("RPC = %v", err)
	}
}

// TestPBDEvidenceUnwiredRefuses proves default refusal and nil-setter behavior.
func TestPBDEvidenceUnwiredRefuses(t *testing.T) {
	installEvidenceTestReader(t, refusingEvidenceReader{})
	assertEvidenceSentinels(t)
	var out, stderr bytes.Buffer
	if err := RunEvidence(context.Background(), []string{"T-1"}, &out, &stderr); err != ErrEvidenceUnwired {
		t.Fatalf("command = %v", err)
	}
	if _, err := EvidenceRPC(context.Background(), json.RawMessage(`{"ticket":"T-1"}`)); err != ErrEvidenceUnwired {
		t.Fatalf("RPC = %v", err)
	}
	if err := snapshotEvidenceReader().Watch(context.Background(), "T-1", nil); err != ErrEvidenceUnwired {
		t.Fatalf("Watch = %v", err)
	}
	r := evidenceTestReader{rows: evidenceTestRows()}
	if err := SetEvidenceReader(r); err != nil {
		t.Fatal(err)
	}
	if err := SetEvidenceReader(nil); err != ErrEvidenceReaderNil {
		t.Fatalf("nil reader = %v", err)
	}
	if err := RunEvidence(context.Background(), []string{"T-1"}, &out, &stderr); err != nil || !strings.Contains(out.String(), "artifact-1") {
		t.Fatalf("reader was replaced: %v, %q", err, out.String())
	}
}

func assertEvidenceSentinels(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		err  error
		msg  string
		kind cascade.Kind
	}{
		{ErrEvidenceUnwired, "pbd evidence is not wired in this build", cascade.KindUnavailable},
		{ErrEvidenceReaderNil, "pbd: evidence reader must not be nil", cascade.KindInvalidInput},
		{ErrEvidenceTicketRequired, "pbd evidence: a ticket argument is required", cascade.KindInvalidInput},
	} {
		if tc.err.Error() != tc.kind.String()+": "+tc.msg || !cascade.HasKind(tc.err, tc.kind) {
			t.Errorf("sentinel = %v, want %q with kind %v", tc.err, tc.msg, tc.kind)
		}
	}
}

// TestPBDEvidenceWatch proves snapshot ordering, emitted rows, and termination.
func TestPBDEvidenceWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rows := evidenceTestRows()
	emitted := []EvidenceEntry{{Seq: 3, Kind: "third"}, {Seq: 4, Kind: "fourth"}}
	installEvidenceTestReader(t, evidenceTestReader{rows: rows, emitted: emitted, cancel: cancel})
	var out, stderr bytes.Buffer
	if err := RunEvidence(ctx, []string{"--watch", "T-1"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %q", out.String())
	}
	for i, prefix := range []string{"1 test", "2 test", "3 third", "4 fourth"} {
		if !strings.HasPrefix(lines[i+1], prefix) {
			t.Errorf("line %d = %q", i, lines[i+1])
		}
	}
	down := cascade.New(cascade.KindUnavailable, "watch down")
	if err := SetEvidenceReader(evidenceTestReader{watchErr: down}); err != nil {
		t.Fatal(err)
	}
	if err := RunEvidence(ctx, []string{"T-1", "--watch"}, &out, &stderr); err != down {
		t.Fatalf("watch error = %v", err)
	}
}
