// Package background provides subagent trace reading and single-writer semantics.
package background

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrTranscriptRead is returned when attempting to read a trace from a transcript file.
var ErrTranscriptRead = errors.New("reading traces from transcript files is not allowed")

// ErrDualWrite is returned when two concurrent writers attempt to append to the same file.
var ErrDualWrite = errors.New("dual write detected, only a single writer is allowed")

// Trace represents a subagent trace record.
type Trace struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

var (
	mu      sync.Mutex
	writers = make(map[string]bool)
)

// ReadTrace reads a trace from a .jsonl or .meta.json file.
// It returns ErrTranscriptRead if the path does not have an allowed extension.
func ReadTrace(path string) (Trace, error) {
	ext := filepath.Ext(path)
	base := filepath.Base(path)

	if ext == ".jsonl" || strings.HasSuffix(base, ".meta.json") {
		data, err := os.ReadFile(path)
		if err != nil {
			return Trace{}, err
		}

		var t Trace
		if ext == ".jsonl" {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == 0 || lines[0] == "" {
				return Trace{}, errors.New("empty jsonl file")
			}
			if err := json.Unmarshal([]byte(lines[0]), &t); err != nil {
				return Trace{}, err
			}
		} else {
			if err := json.Unmarshal(data, &t); err != nil {
				return Trace{}, err
			}
		}
		return t, nil
	}

	return Trace{}, ErrTranscriptRead
}

// AppendSpawn appends a trace record to the specified JSONL file.
// It enforces single-writer semantics; concurrent writes return ErrDualWrite.
func AppendSpawn(path string, t Trace) (err error) {
	mu.Lock()
	if writers[path] {
		mu.Unlock()
		return ErrDualWrite
	}
	writers[path] = true
	mu.Unlock()

	defer func() {
		mu.Lock()
		delete(writers, path)
		mu.Unlock()
	}()

	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	_, err = f.Write(data)
	return err
}

// LockWriter registers a writer lock for the given path to enforce single-writer semantics.
// It returns ErrDualWrite if another writer already holds the lock.
func LockWriter(path string) error {
	mu.Lock()
	defer mu.Unlock()
	if writers[path] {
		return ErrDualWrite
	}
	writers[path] = true
	return nil
}
