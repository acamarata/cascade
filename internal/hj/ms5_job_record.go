// Package hj provides internal job record tracking for MS5.
package hj

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

// ErrInvalidPromptCommand is returned when the provided command string
// does not conform to the required format for MS5 job recording.
var ErrInvalidPromptCommand = errors.New("ms5: invalid prompt command")

// MS5JobRecord represents a single recorded MS5 job entry.
type MS5JobRecord struct {
	ID      string
	Command string
}

var (
	mu      sync.Mutex
	records []MS5JobRecord
	nextID  = 1
)

// RecordMS5Job parses, masks, and records an MS5 job command.
// It rejects commands containing ASCII single quotes, double quotes, or backslashes.
// The command must contain exactly one "--prompt" flag followed by a value token
// that does not begin with "--". The value is masked unless it is exactly "MASKED".
func RecordMS5Job(cmd string) (MS5JobRecord, error) {
	if strings.ContainsAny(cmd, "'\"\\") {
		return MS5JobRecord{}, ErrInvalidPromptCommand
	}

	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return MS5JobRecord{}, ErrInvalidPromptCommand
	}

	promptIdx, err := findPromptIdx(tokens)
	if err != nil {
		return MS5JobRecord{}, err
	}

	value := tokens[promptIdx+1]
	if strings.HasPrefix(value, "--") {
		return MS5JobRecord{}, ErrInvalidPromptCommand
	}

	tokens[promptIdx+1] = maskValue(value)
	maskedCmd := strings.Join(tokens, " ")

	mu.Lock()
	defer mu.Unlock()

	rec := MS5JobRecord{
		ID:      strconv.Itoa(nextID),
		Command: maskedCmd,
	}
	records = append(records, rec)
	nextID++

	return rec, nil
}

func findPromptIdx(tokens []string) (int, error) {
	promptIdx := -1
	promptCount := 0
	for i, t := range tokens {
		if t == "--prompt" {
			promptIdx = i
			promptCount++
		}
	}
	if promptCount != 1 {
		return -1, ErrInvalidPromptCommand
	}
	if promptIdx == 0 || promptIdx != len(tokens)-2 {
		return -1, ErrInvalidPromptCommand
	}
	return promptIdx, nil
}

func maskValue(value string) string {
	if value == "MASKED" {
		return "MASKED"
	}
	return strings.Repeat("*", len(value)-1)
}

// MS5Records returns an independent, insertion-ordered copy of all recorded MS5 jobs.
// It returns nil if no jobs have been successfully recorded yet.
func MS5Records() []MS5JobRecord {
	mu.Lock()
	defer mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	result := make([]MS5JobRecord, len(records))
	copy(result, records)
	return result
}
