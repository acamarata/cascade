// Package docket provides snapshot persistence for refused ticket marks.
package docket

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// ErrInvalidSnapshot is returned when snapshot input is invalid.
var ErrInvalidSnapshot = errors.New("snapshot: invalid input")

// Snapshot holds ticket IDs and their refused marks.
type Snapshot struct {
	TicketIDs    []string        `json:"ticket_ids"`
	RefusedMarks map[string]bool `json:"refused_marks"`
}

// Plan holds a set of ticket IDs for planning.
type Plan struct {
	TicketIDs []string
}

// SaveSnapshot writes the snapshot as a JSON object followed by a newline.
func SaveSnapshot(w io.Writer, s Snapshot) error {
	if w == nil {
		return wrapErr(ErrInvalidSnapshot)
	}

	ids := s.TicketIDs
	if ids == nil {
		ids = []string{}
	}
	marks := s.RefusedMarks
	if marks == nil {
		marks = map[string]bool{}
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return wrapErr(ErrInvalidSnapshot)
		}
		if _, dup := seen[id]; dup {
			return wrapErr(ErrInvalidSnapshot)
		}
		seen[id] = struct{}{}
	}
	for k := range marks {
		if k == "" {
			return wrapErr(ErrInvalidSnapshot)
		}
	}

	normalized := Snapshot{TicketIDs: ids, RefusedMarks: marks}
	data, err := json.Marshal(normalized)
	if err != nil {
		return wrapErr(ErrInvalidSnapshot)
	}
	data = append(data, '\n')

	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n < len(data) {
		return io.ErrShortWrite
	}
	return nil
}

// LoadSnapshot reads a JSON snapshot from r and normalizes nil collections.
func LoadSnapshot(r io.Reader) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, wrapErr(ErrInvalidSnapshot)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return Snapshot{}, wrapErr(ErrInvalidSnapshot)
	}

	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return Snapshot{}, wrapErr(ErrInvalidSnapshot)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw struct {
		TicketIDs    *[]string        `json:"ticket_ids"`
		RefusedMarks *map[string]bool `json:"refused_marks"`
	}

	if err := dec.Decode(&raw); err != nil {
		return Snapshot{}, wrapErr(ErrInvalidSnapshot)
	}

	if dec.More() {
		return Snapshot{}, wrapErr(ErrInvalidSnapshot)
	}

	var s Snapshot
	if raw.TicketIDs != nil {
		s.TicketIDs = *raw.TicketIDs
	} else {
		s.TicketIDs = []string{}
	}
	if raw.RefusedMarks != nil {
		s.RefusedMarks = *raw.RefusedMarks
	} else {
		s.RefusedMarks = map[string]bool{}
	}

	if s.TicketIDs == nil {
		s.TicketIDs = []string{}
	}
	if s.RefusedMarks == nil {
		s.RefusedMarks = map[string]bool{}
	}

	seen := make(map[string]struct{}, len(s.TicketIDs))
	for _, id := range s.TicketIDs {
		if id == "" {
			return Snapshot{}, wrapErr(ErrInvalidSnapshot)
		}
		if _, dup := seen[id]; dup {
			return Snapshot{}, wrapErr(ErrInvalidSnapshot)
		}
		seen[id] = struct{}{}
	}
	for k := range s.RefusedMarks {
		if k == "" {
			return Snapshot{}, wrapErr(ErrInvalidSnapshot)
		}
	}

	return s, nil
}

func wrapErr(err error) error {
	return errors.Join(err, ErrInvalidSnapshot)
}
