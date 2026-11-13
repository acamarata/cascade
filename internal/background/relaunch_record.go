// Package background provides relaunch recording utilities.
package background

import (
	"errors"
	"strings"
)

// Role represents the role of a component being relaunched.
type Role string

const (
	// RoleManager identifies the manager role.
	RoleManager Role = "manager"
	// RoleSupervisor identifies the supervisor role.
	RoleSupervisor Role = "supervisor"
)

// RelaunchEntry records a single relaunch event.
type RelaunchEntry struct {
	// Role is the role that was relaunched.
	Role Role
	// BinaryID is the identifier of the binary.
	BinaryID string
	// Code is the diagnostic code for the relaunch.
	Code string
}

// ManagerLog is the interface used to persist relaunch entries.
type ManagerLog interface {
	// AppendRelaunch appends a relaunch entry to the log.
	AppendRelaunch(entry RelaunchEntry) error
}

var (
	// ErrSupervisorRelaunchForbidden indicates a supervisor relaunch is not allowed.
	ErrSupervisorRelaunchForbidden = errors.New("supervisor relaunch forbidden")
	// ErrManagerLogUnavailable indicates the manager log is nil or an append failed.
	ErrManagerLogUnavailable = errors.New("manager log unavailable")
	// ErrInvalidRelaunch indicates invalid inputs were provided.
	ErrInvalidRelaunch = errors.New("invalid relaunch")
)

// RecordStaleBinaryRelaunch records a stale binary relaunch for the given role.
// If the role is RoleSupervisor, it returns ErrSupervisorRelaunchForbidden immediately
// without inspecting or calling the log, as supervisor running state is caller-owned.
func RecordStaleBinaryRelaunch(role Role, binaryID string, log ManagerLog) error {
	if role == RoleSupervisor {
		return ErrSupervisorRelaunchForbidden
	}

	switch role {
	case RoleSupervisor:
		return ErrSupervisorRelaunchForbidden
	case RoleManager:
		if strings.TrimSpace(binaryID) == "" {
			return ErrInvalidRelaunch
		}
		if log == nil {
			return ErrManagerLogUnavailable
		}
		entry := RelaunchEntry{
			Role:     RoleManager,
			BinaryID: binaryID,
			Code:     "D1094",
		}
		if err := log.AppendRelaunch(entry); err != nil {
			return errors.Join(ErrManagerLogUnavailable, err)
		}
		return nil
	default:
		return ErrInvalidRelaunch
	}
}
