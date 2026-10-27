// Purpose: tests for the audit_chain check's handling of a store that is
//
//	changing under it: a writer between the immutable open and the
//	verification, an empty lone -wal, and a store whose kv table was dropped.
//
// Constraints: no timing; the writer runs in a test-only hook.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (tests, P1-SEC-12).
package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/providers/sqlite"
	_ "modernc.org/sqlite"
)

// TestDoctorAuditChainSurvivesAWriterBetweenOpenAndVerify: a writer appends
// and checkpoints after the immutable open; the first verification may fail
// on the half-changed file, and that must not be reported as tampering.
func TestDoctorAuditChainSurvivesAWriterBetweenOpenAndVerify(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 3)
	closeStoreClean(t, drv, paths)
	ran := 0
	doctorBetweenOpenAndVerify = func() {
		ran++
		w, err := sqlite.Open(context.Background(), auditDBPath(paths))
		if err != nil {
			t.Errorf("the hook writer could not open: %v", err)
			return
		}
		log := audit.New(w, doctorTestClock(), nil)
		for i := 0; i < 1500; i++ {
			ev := audit.Event{Kind: audit.KindPolicyDecide, Actor: "user", Action: "read", Verdict: "allow", ParamsHash: audit.HashParams([]byte{byte(i), byte(i >> 8)})}
			if _, err := log.Append(context.Background(), ev); err != nil {
				t.Errorf("hook append %d: %v", i, err)
				break
			}
		}
		_ = w.Close()
	}
	t.Cleanup(func() { doctorBetweenOpenAndVerify = nil })
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if ran != 1 {
		t.Fatalf("the hook ran %d times, want 1", ran)
	}
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "verified") {
		t.Fatalf("writer between open and verify: status=%s message=%q, want a verified chain (no false tamper alarm)", res.Status, res.Message)
	}
}

// TestDoctorAuditChainEmptyLoneWALDoesNotHideTamper: a zero-length lone -wal
// counts as absent, so a tampered chain is still an error.
func TestDoctorAuditChainEmptyLoneWALDoesNotHideTamper(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 3)
	ctx := context.Background()
	it, err := drv.Scan(ctx, "audit", "rec:")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var key string
	for it.Next(ctx) {
		if key == "" {
			key = it.Key()
		}
	}
	_ = it.Close()
	raw, _ := drv.Get(ctx, "audit", key)
	if err := drv.Put(ctx, "audit", key, []byte(strings.Replace(string(raw), `"actor":"user"`, `"actor":"root"`, 1))); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	closeStoreClean(t, drv, paths)
	if err := os.WriteFile(auditDBPath(paths)+"-wal", nil, 0o600); err != nil {
		t.Fatalf("seed an empty lone wal: %v", err)
	}
	if res := runSecurityCheck(t, newAuditChainDoctorCheck(paths)); res.Status != doctor.StatusError {
		t.Fatalf("tampered chain with an empty lone -wal: status=%s message=%q, want error", res.Status, res.Message)
	}
}

// TestDoctorAuditChainDroppedKVTableIsError: a database that has other tables
// but no kv table is foreign or tampered (error); only a database with no
// tables at all is the creation race (warning).
func TestDoctorAuditChainDroppedKVTableIsError(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 2)
	closeStoreClean(t, drv, paths)
	db, err := sql.Open("sqlite", "file:"+auditDBPath(paths))
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	for _, stmt := range []string{`DROP TABLE kv`, `CREATE TABLE other (x INTEGER)`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = db.Close()
	for _, s := range []string{"-wal", "-shm"} {
		_ = os.Remove(auditDBPath(paths) + s)
	}
	if res := runSecurityCheck(t, newAuditChainDoctorCheck(paths)); res.Status != doctor.StatusError || !strings.Contains(res.Message, "no kv table") {
		t.Fatalf("dropped kv table: status=%s message=%q, want an error", res.Status, res.Message)
	}
}
