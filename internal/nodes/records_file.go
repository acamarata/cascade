package nodes

// Purpose: the production RecordBackend — one JSON file under
//   <data_dir>/nodes/devices.json holding every enrolled device record.
// Inputs: the data directory the CLI and daemon composition roots resolve.
// Outputs: the full record map, or a typed integrity error.
// Constraints: split out of records.go to stay under Art.10.3's 300-line
//   cap. A missing file is an EMPTY fleet, not an error — a controller
//   that has enrolled nobody yet is a normal state. Unparseable content
//   is KindIntegrity and is never silently replaced with an empty map,
//   which would quietly de-enroll the whole fleet. Writes go through
//   runtime.WriteBytesAtomic so a crash mid-save cannot truncate it.
//   Load and Save serialize per path inside the process and retry a
//   transient Windows sharing conflict (records_file_retry.go).
// SPORT: internal/nodes records file backend (CHG) — P1-E17-W4-S37-T1.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fileRecordBackend is the production RecordBackend: one JSON file under
// dataDir/nodes/devices.json (mirrors internal/elevation's fileBackend
// precedent).
type fileRecordBackend struct {
	path  string
	sleep func(time.Duration) // backoff between retries; time.Sleep in production
}

// NewFileRecordBackend returns a RecordBackend that persists at
// <dataDir>/nodes/devices.json.
func NewFileRecordBackend(dataDir string) RecordBackend {
	return fileRecordBackend{path: filepath.Join(dataDir, "nodes", "devices.json"), sleep: time.Sleep}
}

// Load reads the store under the path's shared lock, retrying a bounded
// number of times on a transient Windows sharing conflict (see
// records_file_retry.go). Any other error, and a conflict that outlasts
// the retry budget, is returned unchanged.
func (b fileRecordBackend) Load() (map[string]DeviceRecord, error) {
	mu := recordFileLock(b.path)
	mu.RLock()
	defer mu.RUnlock()
	var data []byte
	err := retryRecordFileOp(isTransientShareError, b.sleep, func() error {
		var readErr error
		data, readErr = readRecordFile(b.path)
		return readErr
	})
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]DeviceRecord{}, nil
		}
		return nil, err
	}
	var records map[string]DeviceRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "nodes: device record store is not valid JSON")
	}
	if records == nil {
		records = map[string]DeviceRecord{}
	}
	return records, nil
}

// Save publishes the full set under the path's exclusive lock, with the
// same bounded retry as Load: the atomic rename onto devices.json fails
// on Windows while another process briefly holds the file open.
func (b fileRecordBackend) Save(records map[string]DeviceRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	mu := recordFileLock(b.path)
	mu.Lock()
	defer mu.Unlock()
	return retryRecordFileOp(isTransientShareError, b.sleep, func() error {
		return runtime.WriteBytesAtomic(b.path, data)
	})
}
