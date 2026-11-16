package nodes

import "sync"

// memRecordBackend is an in-memory RecordBackend fake for tests. Also used
// to simulate a corrupt/unavailable store. mu guards records: the node
// channel's reconcile loop reads while heartbeats write.
type memRecordBackend struct {
	mu      sync.Mutex
	records map[string]DeviceRecord
	loadErr error
	saveErr error
}

func newMemRecordBackend() *memRecordBackend {
	return &memRecordBackend{records: map[string]DeviceRecord{}}
}

func (m *memRecordBackend) Load() (map[string]DeviceRecord, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]DeviceRecord{}
	for k, v := range m.records {
		out[k] = v
	}
	return out, nil
}

func (m *memRecordBackend) Save(records map[string]DeviceRecord) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = records
	return nil
}
