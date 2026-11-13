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
}

func TestRecordManagerRelaunchRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		role     Role
		binaryID string
	}{
		{"empty binaryID", RoleManager, ""},
		{"blank binaryID with whitespace", RoleManager, "   "},
		{"unknown role", "unknown", "some-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &mockManagerLog{}
			err := RecordStaleBinaryRelaunch(tc.role, tc.binaryID, l)
			if !errors.Is(err, ErrInvalidRelaunch) {
				t.Errorf("expected ErrInvalidRelaunch, got %v", err)
			}
			if len(l.entries) != 0 {
				t.Errorf("expected no entries, got %d", len(l.entries))
			}
		})
	}
}

func TestRecordManagerRelaunchLogAndRoleErrors(t *testing.T) {
	// Nil log
	err := RecordStaleBinaryRelaunch(RoleManager, "cascade-old", nil)
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
