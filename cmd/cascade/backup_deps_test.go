// Purpose: unit coverage for productionBackupDeps' top-level assembly --
//
//	this file shipped with no test that calls the function directly (only
//	comments in other test files referencing its shape), so the field
//	assignment statements themselves were never exercised.
//
// SPORT: cmd.cascade.backup/TEST (P1-E19-W4-S42-T3).
package main

import "testing"

func TestProductionBackupDeps_AssemblesEveryField(t *testing.T) {
	deps := productionBackupDeps()
	switch {
	case deps.Open == nil:
		t.Error("productionBackupDeps: Open is nil")
	case deps.BuildTarget == nil:
		t.Error("productionBackupDeps: BuildTarget is nil")
	case deps.Authorize == nil:
		t.Error("productionBackupDeps: Authorize is nil")
	case deps.Clock == nil:
		t.Error("productionBackupDeps: Clock is nil")
	case deps.Getenv == nil:
		t.Error("productionBackupDeps: Getenv is nil")
	case deps.ReadFile == nil:
		t.Error("productionBackupDeps: ReadFile is nil")
	case deps.WriteFile == nil:
		t.Error("productionBackupDeps: WriteFile is nil")
	case deps.NewVault == nil:
		t.Error("productionBackupDeps: NewVault is nil")
	case deps.Create == nil:
		t.Error("productionBackupDeps: Create is nil")
	case deps.Restore == nil:
		t.Error("productionBackupDeps: Restore is nil")
	case deps.Export == nil:
		t.Error("productionBackupDeps: Export is nil")
	case deps.Import == nil:
		t.Error("productionBackupDeps: Import is nil")
	case deps.AuditLog == nil:
		t.Error("productionBackupDeps: AuditLog is nil")
	case deps.Verify == nil:
		t.Error("productionBackupDeps: Verify is nil")
	case deps.NewAttentionSink == nil:
		t.Error("productionBackupDeps: NewAttentionSink is nil")
	}
}
