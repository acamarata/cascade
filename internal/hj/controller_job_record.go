// Package hj provides internal job record tracking for the controller.
package hj

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

// ErrInvalidPromptCommand is returned when the provided command string
// does not conform to the required format for controller job recording.
var ErrInvalidPromptCommand = errors.New("controller: invalid prompt command")

// ControllerJobRecord represents a single recorded controller job entry.
type ControllerJobRecord struct {
	ID      string
	Command string
}

var (
	mu      sync.Mutex
	records []ControllerJobRecord
	nextID  = 1
)

// RecordControllerJob parses, masks, and records a controller job command.
// It rejects commands containing ASCII single quotes, double quotes, or backslashes.
// The command must contain exactly one "--prompt" flag followed by a value token
// that does not begin with "--". The value is masked unless it is exactly "MASKED".
func RecordControllerJob(cmd string) (ControllerJobRecord, error) {
	if strings.ContainsAny(cmd, "'\"\\") {
		return ControllerJobRecord{}, ErrInvalidPromptCommand
	}

	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return ControllerJobRecord{}, ErrInvalidPromptCommand
	}

	promptIdx, err := findPromptIdx(tokens)
	if err != nil {
		return ControllerJobRecord{}, err
	}

	value := tokens[promptIdx+1]
	if strings.HasPrefix(value, "--") {
		return ControllerJobRecord{}, ErrInvalidPromptCommand
	}

	tokens[promptIdx+1] = maskValue(value)
	maskedCmd := strings.Join(tokens, " ")

	mu.Lock()
	defer mu.Unlock()

	rec := ControllerJobRecord{
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

// ControllerRecords returns an independent, insertion-ordered copy of all recorded controller jobs.
// It returns nil if no jobs have been successfully recorded yet.
func ControllerRecords() []ControllerJobRecord {
	mu.Lock()
	defer mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	result := make([]ControllerJobRecord, len(records))
	copy(result, records)
	return result
}
