// Package progress provides types and functions for recording build progress.
package progress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrInvalidChange is returned when a Change fails validation.
var ErrInvalidChange = errors.New("D2242: invalid progress change")

// ErrAppendChange is returned when appending a change to the progress file fails.
var ErrAppendChange = errors.New("D2289: append progress change failed")

// Change represents a single progress state transition for a ticket.
type Change struct {
	Ticket  string `json:"ticket"`
	Status  string `json:"status"`
	Attempt int    `json:"attempt"`
	Label   string `json:"label"`
}

var validStatuses = map[string]bool{
	"provisional": true,
	"ready":       true,
	"running":     true,
	"review":      true,
	"landed":      true,
	"parked":      true,
	"blocked":     true,
	"cancelled":   true,
	"needs-owner": true,
}

// AppendChange validates c, marshals it as JSON with a trailing newline,
// and appends exactly one line to the file at path.
func AppendChange(path string, c Change) error {
	if strings.TrimSpace(c.Ticket) == "" {
		return ErrInvalidChange
	}
	if c.Attempt < 1 {
		return ErrInvalidChange
	}
	if !validStatuses[c.Status] {
		return ErrInvalidChange
	}
	if path == "" {
		return ErrInvalidChange
	}

	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppendChange, err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppendChange, err)
	}
	// (*os.File).Write returns a non-nil error whenever it writes fewer bytes than given, so no short-write branch.
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: %v", ErrAppendChange, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrAppendChange, err)
	}
	return nil
}
