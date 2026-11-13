package jobs

import "errors"

// LogReadResult represents the result of a bounded job log read.
// It contains the possibly truncated data, the true file size,
// and a flag indicating whether truncation occurred.
type LogReadResult struct {
	Data     []byte
	TrueSize int64
	Cut      bool
}

// ErrInvalidMax is returned when the maximum read size is less than 1.
var ErrInvalidMax = errors.New("max must be >= 1")

// ErrUnknownJobID is returned when the requested job ID does not exist.
var ErrUnknownJobID = errors.New("unknown job id")
