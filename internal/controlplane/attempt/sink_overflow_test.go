package attempt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeBase struct {
	mu      sync.Mutex
	records []AttemptRecord
	closed  int
	err     error
}

func (f *fakeBase) Record(r AttemptRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, r)
}

func (f *fakeBase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.err
}

func validRow() OverflowRow {
	return OverflowRow{
		TS:      "2024-05-01T12:00:00.123456789Z",
		Ticket:  "T-1",
		Attempt: 1,
		Reason:  "overflow",
		Dropped: 0,
	}
}

func TestAppendOverflowRow(t *testing.T) {
	dir := t.TempDir()
	base := &fakeBase{err: errors.New("base close error")}
	sink, err := NewOverflowSink(base, dir)
	if err != nil {
		t.Fatalf("NewOverflowSink: %v", err)
	}

	// Record delegates
	rec := AttemptRecord{Lane: "L1", Model: "M1", Node: "N1", Outcome: "O1", Cause: "C1", SHA: "S1"}
	sink.Record(rec)
	if len(base.records) != 1 || base.records[0] != rec {
		t.Errorf("Record did not delegate correctly")
	}

	// RecordOverflow
	err = sink.RecordOverflow(validRow())
	if err != nil {
		t.Fatalf("RecordOverflow: %v", err)
	}

	// Close
	err = sink.Close()
	if !errors.Is(err, base.err) {
		t.Errorf("Close error: %v", err)
	}
	if base.closed != 1 {
		t.Errorf("base.Close called %d times", base.closed)
	}

	// Repeated Close
	err2 := sink.Close()
	if !errors.Is(err2, base.err) {
		t.Errorf("Repeated Close error: %v", err2)
	}
	if base.closed != 1 {
		t.Errorf("base.Close called %d times after repeated Close", base.closed)
	}

	// RecordOverflow after Close
	err = sink.RecordOverflow(validRow())
	if !errors.Is(err, ErrOverflowClosed) {
		t.Errorf("RecordOverflow after Close: %v", err)
	}

	// Verify JSON line
	path := filepath.Join(dir, "escalations.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(lines[0], &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["ticket"] != "T-1" {
		t.Errorf("ticket: %v", m["ticket"])
	}
}

func TestAppendMultipleRows(t *testing.T) {
	dir := t.TempDir()
	// Create existing file with one line
	path := filepath.Join(dir, "escalations.jsonl")
	existing := `{"ts":"2024-01-01T00:00:00Z","ticket":"OLD","attempt":1,"reason":"old","dropped":0}` + "\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	base := &fakeBase{}
	sink, err := NewOverflowSink(base, dir)
	if err != nil {
		t.Fatalf("NewOverflowSink: %v", err)
	}

	row1 := validRow()
	row1.Ticket = "T-1"
	row2 := validRow()
	row2.Ticket = "T-2"
	row3 := validRow()
	row3.Ticket = "T-3"

	for _, r := range []OverflowRow{row1, row2, row3} {
		if err := sink.RecordOverflow(r); err != nil {
			t.Fatalf("RecordOverflow: %v", err)
		}
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != strings.TrimSpace(existing) {
		t.Errorf("first line overwritten or modified: %s", lines[0])
	}
	for i, tk := range []string{"T-1", "T-2", "T-3"} {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i+1]), &m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if m["ticket"] != tk {
			t.Errorf("line %d: expected ticket %s, got %v", i+1, tk, m["ticket"])
		}
	}
}
