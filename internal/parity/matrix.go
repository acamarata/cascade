// Package parity provides types and utilities for managing parity matrices.
package parity

import (
	"encoding/json"
	"errors"
	"io"
)

var (
	// ErrDuplicateID is returned when multiple rows share the same ID.
	ErrDuplicateID = errors.New("duplicate row ID")
	// ErrUnknownStatus is returned when a row has an invalid status.
	ErrUnknownStatus = errors.New("unknown status")
	// ErrWaivedNoReason is returned when a waived row lacks a reason.
	ErrWaivedNoReason = errors.New("waived status requires a reason")
)

// Row represents a single entry in the parity matrix.
type Row struct {
	ID         string `json:"id"`
	Area       string `json:"area"`
	CappOp     string `json:"capp_op"`
	CascadeCmd string `json:"cascade_cmd"`
	Status     string `json:"status"`
	Evidence   string `json:"evidence"`
	Reason     string `json:"reason"`
}

// Matrix represents a collection of parity rows.
type Matrix struct {
	Rows []Row `json:"rows"`
}

// Load reads a JSON document from r and returns a validated Matrix.
func Load(r io.Reader) (Matrix, error) {
	var m Matrix
	dec := json.NewDecoder(r)
	if err := dec.Decode(&m); err != nil {
		return Matrix{}, err
	}
	if err := Validate(m); err != nil {
		return Matrix{}, err
	}
	return m, nil
}

// Validate checks the matrix for structural and semantic errors,
// returning the first error encountered.
func Validate(m Matrix) error {
	seen := make(map[string]bool)
	for _, row := range m.Rows {
		if seen[row.ID] {
			return ErrDuplicateID
		}
		seen[row.ID] = true

		switch row.Status {
		case "parity", "gap", "waived":
			// valid
		default:
			return ErrUnknownStatus
		}

		if row.Status == "waived" && row.Reason == "" {
			return ErrWaivedNoReason
		}
	}
	return nil
}
