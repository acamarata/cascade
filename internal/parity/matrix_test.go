package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestLoadRoundTrip(t *testing.T) {
	orig := Matrix{
		Rows: []Row{
			{ID: "r1", Area: "a1", CappOp: "c1", CascadeCmd: "cmd1", Status: "parity", Evidence: "e1", Reason: ""},
			{ID: "r2", Area: "a2", CappOp: "c2", CascadeCmd: "cmd2", Status: "gap", Evidence: "e2", Reason: ""},
			{ID: "r3", Area: "a3", CappOp: "c3", CascadeCmd: "cmd3", Status: "waived", Evidence: "e3", Reason: "because"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got.Rows))
	}
	for i, r := range got.Rows {
		if r != orig.Rows[i] {
			t.Errorf("row %d mismatch: got %+v, want %+v", i, r, orig.Rows[i])
		}
	}
}

func TestValidateRejectsDuplicateID(t *testing.T) {
	m := Matrix{
		Rows: []Row{
			{ID: "r1", Status: "parity"},
			{ID: "r1", Status: "gap"},
		},
	}
	err := Validate(m)
	if !errors.Is(err, ErrDuplicateID) {
		t.Errorf("expected ErrDuplicateID, got %v", err)
	}
}

func TestValidateRejectsUnknownStatus(t *testing.T) {
	m := Matrix{
		Rows: []Row{
			{ID: "r1", Status: "done"},
		},
	}
	err := Validate(m)
	if !errors.Is(err, ErrUnknownStatus) {
		t.Errorf("expected ErrUnknownStatus, got %v", err)
	}
}

func TestValidateWaivedNeedsReason(t *testing.T) {
	m := Matrix{
		Rows: []Row{
			{ID: "r1", Status: "waived", Reason: ""},
		},
	}
	err := Validate(m)
	if !errors.Is(err, ErrWaivedNoReason) {
		t.Errorf("expected ErrWaivedNoReason, got %v", err)
	}

	m.Rows[0].Reason = "good reason"
	err = Validate(m)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
