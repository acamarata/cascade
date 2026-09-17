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
// SPORT: internal/nodes records file backend (CHG) — P1-E17-W4-S37-T1.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fileRecordBackend is the production RecordBackend: one JSON file under
// dataDir/nodes/devices.json (mirrors internal/elevation's fileBackend
// precedent).
type fileRecordBackend struct {
	path string
}

// NewFileRecordBackend returns a RecordBackend that persists at
// <dataDir>/nodes/devices.json.
func NewFileRecordBackend(dataDir string) RecordBackend {
	return fileRecordBackend{path: filepath.Join(dataDir, "nodes", "devices.json")}
}

func (b fileRecordBackend) Load() (map[string]DeviceRecord, error) {
	data, err := os.ReadFile(b.path)
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

func (b fileRecordBackend) Save(records map[string]DeviceRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return runtime.WriteBytesAtomic(b.path, data)
}
