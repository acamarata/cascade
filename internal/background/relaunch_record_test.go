package background

import (
	"errors"
	"testing"
)

type mockManagerLog struct {
	entries []RelaunchEntry
	err     error
}

func (m *mockManagerLog) AppendRelaunch(entry RelaunchEntry) error {
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, entry)
	return nil
}

func TestRecordManagerRelaunchUpdatesManagerLog(t *testing.T) {
	// Valid manager relaunch
	log := &mockManagerLog{}
	err := RecordStaleBinaryRelaunch(RoleManager, "cascade-old", log)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(log.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(log.entries))
	}
	e := log.entries[0]
	if e.Role != RoleManager {
		t.Errorf("expected role %s, got %s", RoleManager, e.Role)
	}
	if e.BinaryID != "cascade-old" {
		t.Errorf("expected binaryID 'cascade-old', got %s", e.BinaryID)
	}
	if e.Code != "D1094" {
		t.Errorf("expected code 'D1094', got %s", e.Code)
	}

	// Empty binaryID
	log2 := &mockManagerLog{}
	err = RecordStaleBinaryRelaunch(RoleManager, "", log2)
	if !errors.Is(err, ErrInvalidRelaunch) {
		t.Errorf("expected ErrInvalidRelaunch, got %v", err)
	}
	if len(log2.entries) != 0 {
		t.Errorf("expected no entries, got %d", len(log2.entries))
	}

	// Blank binaryID with whitespace
	log3 := &mockManagerLog{}
	err = RecordStaleBinaryRelaunch(RoleManager, "   ", log3)
	if !errors.Is(err, ErrInvalidRelaunch) {
		t.Errorf("expected ErrInvalidRelaunch, got %v", err)
	}
	if len(log3.entries) != 0 {
		t.Errorf("expected no entries, got %d", len(log3.entries))
	}

	// Unknown role
	log4 := &mockManagerLog{}
	err = RecordStaleBinaryRelaunch("unknown", "some-id", log4)
	if !errors.Is(err, ErrInvalidRelaunch) {
		t.Errorf("expected ErrInvalidRelaunch, got %v", err)
	}
	if len(log4.entries) != 0 {
		t.Errorf("expected no entries, got %d", len(log4.entries))
	}

	// Nil log
	err = RecordStaleBinaryRelaunch(RoleManager, "cascade-old", nil)
	if !errors.Is(err, ErrManagerLogUnavailable) {
		t.Errorf("expected ErrManagerLogUnavailable, got %v", err)
	}

	// Supervisor role
	err = RecordStaleBinaryRelaunch(RoleSupervisor, "some-id", &mockManagerLog{})
	if !errors.Is(err, ErrSupervisorRelaunchForbidden) {
		t.Errorf("expected ErrSupervisorRelaunchForbidden, got %v", err)
	}

	// Append error wraps cause
	appendErr := errors.New("disk full")
	log6 := &mockManagerLog{err: appendErr}
	err = RecordStaleBinaryRelaunch(RoleManager, "cascade-old", log6)
	if !errors.Is(err, ErrManagerLogUnavailable) {
		t.Errorf("expected ErrManagerLogUnavailable on append error, got %v", err)
	}
	if !errors.Is(err, appendErr) {
		t.Errorf("expected wrapped cause %v, got %v", appendErr, err)
	}
}
