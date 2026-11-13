package log

import (
	"errors"
	"reflect"
	"strings"
)

// Starter is an interface for starting a flow run.
type Starter interface {
	Start(runID string, tickets []string, logPath string) (pid int, err error)
}

// RunInfo contains information about a flow run.
type RunInfo struct {
	RunID   string
	PID     int
	LogPath string
}

var (
	// ErrInvalidStarter is returned when the provided Starter is nil or a typed nil.
	ErrInvalidStarter = errors.New("invalid starter")
	// ErrInvalidRunID is returned when the runID is invalid.
	ErrInvalidRunID = errors.New("invalid run id")
	// ErrEmptyPlan is returned when the tickets slice is nil or empty.
	ErrEmptyPlan = errors.New("empty plan")
	// ErrInvalidTicket is returned when a ticket entry is invalid.
	ErrInvalidTicket = errors.New("invalid ticket")
)

// FlowRun validates the inputs for a flow run.
func FlowRun(s Starter, runID string, tickets []string) (RunInfo, error) {
	if s == nil {
		return RunInfo{}, ErrInvalidStarter
	}
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		if v.IsNil() {
			return RunInfo{}, ErrInvalidStarter
		}
	default:
		// other kinds cannot be nil
	}

	if !isValidRunID(runID) {
		return RunInfo{}, ErrInvalidRunID
	}

	if len(tickets) == 0 {
		return RunInfo{}, ErrEmptyPlan
	}

	seen := make(map[string]struct{})
	for _, t := range tickets {
		if t == "" || t != strings.TrimSpace(t) {
			return RunInfo{}, ErrInvalidTicket
		}
		if _, exists := seen[t]; exists {
			return RunInfo{}, ErrInvalidTicket
		}
		seen[t] = struct{}{}
	}

	logPath := "data/gc-flow/" + runID + ".log"
	return RunInfo{
		RunID:   runID,
		PID:     0,
		LogPath: logPath,
	}, nil
}

func isValidRunID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if r > 127 {
			return false
		}
		alnum := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if i == 0 {
			if !alnum {
				return false
			}
		} else if !alnum && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
