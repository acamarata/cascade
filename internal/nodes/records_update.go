package nodes

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recordLocker serializes the entire read-modify-write across stores.
type recordLocker interface {
	lockRecords() (unlock func(), err error)
}

var recordLockWait = 10 * time.Second

// ErrRecordStoreLocked reports that another writer outlasted the lock budget.
func ErrRecordStoreLocked() error {
	return cascade.New(cascade.KindUnavailable, "nodes: device record store is locked by another process")
}

// mutate holds the backend lock across Load, fn and Save. Callbacks must
// only edit records: no I/O, signing or re-entry into a store method.
func (s *RecordStore) mutate(fn func(map[string]DeviceRecord) error) error {
	if locker, ok := s.backend.(recordLocker); ok {
		unlock, err := locker.lockRecords()
		if err != nil {
			return err
		}
		defer unlock()
	} else {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	records, err := s.backend.Load()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: read device records")
	}
	if err := fn(records); err != nil {
		return err
	}
	if err := s.backend.Save(records); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: write device records")
	}
	return nil
}

// update applies fn to the fresh record under the lock. A missing record
// or callback error leaves the stored bytes untouched and returns no record.
func (s *RecordStore) update(nodeID string, fn func(*DeviceRecord) error) (DeviceRecord, error) {
	var rec DeviceRecord
	err := s.mutate(func(records map[string]DeviceRecord) error {
		var ok bool
		rec, ok = records[nodeID]
		if !ok {
			return cascade.Newf(cascade.KindNotFound, "nodes: no device record for node %q", nodeID)
		}
		if err := fn(&rec); err != nil {
			return err
		}
		records[nodeID] = rec
		return nil
	})
	if err != nil {
		return DeviceRecord{}, err
	}
	return rec, nil
}
