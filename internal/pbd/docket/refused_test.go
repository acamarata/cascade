package docket

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// failWriter returns an error on Write.
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) {
	return 0, f.err
}

// shortWriter writes fewer bytes than requested.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) > 1 {
		return 1, nil
	}
	return len(p), nil
}

func TestSnapshotRoundTripRefused(t *testing.T) {
	tests := []struct {
		name    string
		snap    Snapshot
		wantErr bool
	}{
		{
			name: "T1,T2 true survive",
			snap: Snapshot{
				TicketIDs:    []string{"T1", "T2"},
				RefusedMarks: map[string]bool{"T1": true, "T2": true},
			},
			wantErr: false,
		},
		{
			name:    "empty normalization",
			snap:    Snapshot{},
			wantErr: false,
		},
		{
			name: "false marks",
			snap: Snapshot{
				TicketIDs:    []string{"T3"},
				RefusedMarks: map[string]bool{"T3": false},
			},
			wantErr: false,
		},
		{
			name: "nil slices and maps",
			snap: Snapshot{
				TicketIDs:    nil,
				RefusedMarks: nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := SaveSnapshot(&buf, tt.snap)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SaveSnapshot() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}

			got, err := LoadSnapshot(&buf)
			if err != nil {
				t.Fatalf("LoadSnapshot() error = %v", err)
			}

			if tt.snap.TicketIDs == nil {
				if len(got.TicketIDs) != 0 {
					t.Errorf("TicketIDs not empty, got %v", got.TicketIDs)
				}
			} else {
				if len(got.TicketIDs) != len(tt.snap.TicketIDs) {
					t.Errorf("TicketIDs length mismatch")
				}
			}
			if tt.snap.RefusedMarks == nil {
				if len(got.RefusedMarks) != 0 {
					t.Errorf("RefusedMarks not empty, got %v", got.RefusedMarks)
				}
			} else {
				if len(got.RefusedMarks) != len(tt.snap.RefusedMarks) {
					t.Errorf("RefusedMarks length mismatch")
				}
			}
		})
	}

	t.Run("JSON fields and newline", func(t *testing.T) {
		var buf bytes.Buffer
		s := Snapshot{
			TicketIDs:    []string{"T1", "T2"},
			RefusedMarks: map[string]bool{"T2": true, "T1": false},
		}
		if err := SaveSnapshot(&buf, s); err != nil {
			t.Fatal(err)
		}
		data := buf.String()
		if !strings.HasSuffix(data, "\n") {
			t.Errorf("missing newline")
		}
		if !strings.Contains(data, `"ticket_ids"`) {
			t.Errorf("missing ticket_ids field")
		}
		if !strings.Contains(data, `"refused_marks"`) {
			t.Errorf("missing refused_marks field")
		}
		// sorted map keys in json: T1 then T2
		idx1 := strings.Index(data, `"T1"`)
		idx2 := strings.Index(data, `"T2"`)
		if idx1 > idx2 {
			t.Errorf("map keys not sorted: T1 at %d, T2 at %d", idx1, idx2)
		}
	})

	t.Run("corrupt data", func(t *testing.T) {
		_, err := LoadSnapshot(strings.NewReader("{corrupt"))
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot, got %v", err)
		}
	})

	t.Run("duplicate-key JSON", func(t *testing.T) {
		_, err := LoadSnapshot(strings.NewReader(`{"ticket_ids":["A","A"],"refused_marks":{}}`))
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot for duplicate IDs, got %v", err)
		}
	})

	t.Run("trailing JSON", func(t *testing.T) {
		_, err := LoadSnapshot(strings.NewReader(`{"ticket_ids":[],"refused_marks":{}}{}`))
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot for trailing JSON, got %v", err)
		}
	})

	t.Run("wrong-type data", func(t *testing.T) {
		_, err := LoadSnapshot(strings.NewReader(`{"ticket_ids":"not-an-array","refused_marks":{}}`))
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot for wrong type, got %v", err)
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		_, err := LoadSnapshot(nil)
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot for nil reader, got %v", err)
		}
	})

	t.Run("nil writer", func(t *testing.T) {
		err := SaveSnapshot(nil, Snapshot{})
		if !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("expected ErrInvalidSnapshot for nil writer, got %v", err)
		}
	})

	t.Run("propagated writer error", func(t *testing.T) {
		werr := errors.New("disk full")
		err := SaveSnapshot(failWriter{err: werr}, Snapshot{})
		if !errors.Is(err, werr) {
			t.Errorf("expected writer error to propagate, got %v", err)
		}
	})

	t.Run("short write error", func(t *testing.T) {
		err := SaveSnapshot(shortWriter{}, Snapshot{TicketIDs: []string{"T1"}})
		if !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("expected io.ErrShortWrite, got %v", err)
		}
	})
}
