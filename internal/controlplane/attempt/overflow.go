package attempt

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OverflowRow represents a single overflow event for escalation logging.
type OverflowRow struct {
	TS      string `json:"ts"`
	Ticket  string `json:"ticket"`
	Attempt int    `json:"attempt"`
	Reason  string `json:"reason"`
	Dropped int    `json:"dropped"`
}

// ErrInvalidOverflow is returned when an OverflowRow fails validation.
var ErrInvalidOverflow = errors.New("invalid overflow row")

// ErrOverflowClosed is returned when writing to a closed overflow sink.
var ErrOverflowClosed = errors.New("overflow sink closed")

// ErrOverflowWrite is returned when an append to the overflow file fails.
var ErrOverflowWrite = errors.New("overflow append failed")

// ErrOverflowQueueFull is returned when the overflow queue has no capacity.
var ErrOverflowQueueFull = errors.New("overflow queue full")

// validateOverflowRow checks that all fields of an OverflowRow meet the
// required constraints: TS must parse as RFC3339Nano, Ticket and Reason
// must contain non-whitespace characters, Attempt must be >= 1, and
// Dropped must be >= 0.
func validateOverflowRow(row OverflowRow) error {
	if _, err := time.Parse(time.RFC3339Nano, row.TS); err != nil {
		return ErrInvalidOverflow
	}
	if strings.TrimSpace(row.Ticket) == "" {
		return ErrInvalidOverflow
	}
	if strings.TrimSpace(row.Reason) == "" {
		return ErrInvalidOverflow
	}
	if row.Attempt < 1 {
		return ErrInvalidOverflow
	}
	if row.Dropped < 0 {
		return ErrInvalidOverflow
	}
	return nil
}

// FormatOverflowJSON validates the given OverflowRow and returns its JSON
// encoding followed by exactly one newline character. The output contains
// exactly five keys: ts, ticket, attempt, reason, dropped.
func FormatOverflowJSON(row OverflowRow) ([]byte, error) {
	if err := validateOverflowRow(row); err != nil {
		return nil, err
	}
	data, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data)+1)
	copy(out, data)
	out[len(data)] = '\n'
	return out, nil
}

// AppendOverflowRow validates the row, then appends its JSON representation
// to escalations.jsonl inside the specified directory. The file is opened
// with os.O_CREATE|os.O_WRONLY|os.O_APPEND and mode 0600. No directories
// are created. A blank dir or invalid row returns ErrInvalidOverflow.
// Open, write, or close failures wrap ErrOverflowWrite via errors.Join.
func AppendOverflowRow(dir string, row OverflowRow) error {
	if strings.TrimSpace(dir) == "" {
		return ErrInvalidOverflow
	}
	if err := validateOverflowRow(row); err != nil {
		return err
	}
	data, err := FormatOverflowJSON(row)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "escalations.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return errors.Join(ErrOverflowWrite, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return errors.Join(ErrOverflowWrite, err)
	}
	if err := f.Close(); err != nil {
		return errors.Join(ErrOverflowWrite, err)
	}
	return nil
}
