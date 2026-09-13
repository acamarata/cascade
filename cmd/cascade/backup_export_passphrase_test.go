// Purpose: unit coverage for readBackupPassphrase's three branches
//
//	(empty path, unreadable file, empty file content, and the success
//	trim), which shipped with no direct test of its own.
//
// SPORT: cmd.cascade.backup-export/TEST (P1-E19-W4-S42-T3).
package main

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestReadBackupPassphrase_EmptyPathRefuses(t *testing.T) {
	_, err := readBackupPassphrase(backupDeps{}, "")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("readBackupPassphrase(\"\") = %v, want KindInvalidInput", err)
	}
}

func TestReadBackupPassphrase_UnreadableFileRefuses(t *testing.T) {
	deps := backupDeps{ReadFile: func(string) ([]byte, error) { return nil, errors.New("boom") }}
	_, err := readBackupPassphrase(deps, "/does/not/matter")
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("readBackupPassphrase(unreadable) = %v, want KindUnavailable", err)
	}
}

func TestReadBackupPassphrase_EmptyContentRefuses(t *testing.T) {
	deps := backupDeps{ReadFile: func(string) ([]byte, error) { return []byte("\r\n"), nil }}
	_, err := readBackupPassphrase(deps, "/some/path")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("readBackupPassphrase(empty content) = %v, want KindInvalidInput", err)
	}
}

func TestReadBackupPassphrase_TrimsTrailingNewline(t *testing.T) {
	deps := backupDeps{ReadFile: func(string) ([]byte, error) { return []byte("hunter2\n"), nil }}
	got, err := readBackupPassphrase(deps, "/some/path")
	if err != nil {
		t.Fatalf("readBackupPassphrase: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("readBackupPassphrase = %q, want %q", got, "hunter2")
	}
}
