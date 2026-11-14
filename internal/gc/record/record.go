// Package record provides functionality to read GC service records.
package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Record represents a single entry from the gcproxy.jsonl file.
// It only contains the name and count fields; any other fields
// present in the JSON are dropped and never stored.
type Record struct {
	// Name is the identifier for the record.
	Name string `json:"name"`
	// Count is the numeric value associated with the record.
	Count int `json:"count"`
}

// ReadServiceRecord reads gcproxy.jsonl from the given dataHome directory
// and returns a slice of Record containing only counts and names.
// A missing file returns an empty slice and no error.
// A malformed line returns an error naming the line number.
func ReadServiceRecord(dataHome string) ([]Record, error) {
	path := filepath.Join(dataHome, "gcproxy.jsonl")

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Record{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		records = append(records, r)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return records, nil
}
