// Package docket provides swarm state management utilities.
package docket

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ErrSplitsCorrupt is returned when splits.jsonl contains a malformed line.
var ErrSplitsCorrupt = errors.New("ERR_SPLITS_CORRUPT")

const splitsFile = "splits.jsonl"

// splitEntry represents a single line in the splits ledger.
type splitEntry struct {
	ID string `json:"id"`
}

// RecordSplit appends one JSON line to splits.jsonl in stateDir and returns
// true the first time id is seen. A repeat id appends nothing and returns false.
func RecordSplit(stateDir, id string) (recorded bool, err error) {
	path := filepath.Join(stateDir, splitsFile)

	has, err := HasSplit(stateDir, id)
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			recorded, err = false, cerr
		}
	}()

	entry := splitEntry{ID: id}
	data, err := json.Marshal(entry)
	if err != nil {
		return false, err
	}
	data = append(data, '\n')

	if _, err := f.Write(data); err != nil {
		return false, err
	}

	return true, nil
}

// HasSplit reads splits.jsonl in stateDir and returns true if id has been recorded.
// A missing file returns false, nil. A malformed line returns false, ErrSplitsCorrupt.
func HasSplit(stateDir, id string) (found bool, err error) {
	path := filepath.Join(stateDir, splitsFile)

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			found, err = false, cerr
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry splitEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return false, ErrSplitsCorrupt
		}
		if entry.ID == id {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}

	return false, nil
}
