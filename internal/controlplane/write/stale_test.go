package write

import (
	"testing"
	"time"
)

func TestStaleBinaryExactly30Minutes(t *testing.T) {
	supervisorStart := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC)
	binaryBuild := time.Date(2026, time.November, 13, 10, 30, 0, 0, time.UTC)

	finding, ok := CheckStaleBinary(supervisorStart, binaryBuild)
	if !ok {
		t.Fatal("expected a supervisor_stale_binary finding for a delta of exactly 30 minutes")
	}
	if finding.Kind != "supervisor_stale_binary" {
		t.Fatalf("finding.Kind = %q, want %q", finding.Kind, "supervisor_stale_binary")
	}
	if finding.Code != "D1081" {
		t.Fatalf("finding.Code = %q, want %q", finding.Code, "D1081")
	}
}

func TestStaleBinaryOver30Minutes(t *testing.T) {
	supervisorStart := time.Date(2026, time.November, 13, 9, 0, 0, 0, time.UTC)
	binaryBuild := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC)

	finding, ok := CheckStaleBinary(supervisorStart, binaryBuild)
	if !ok {
		t.Fatal("expected a supervisor_stale_binary finding for a delta over 30 minutes")
	}
	if finding.Kind != "supervisor_stale_binary" {
		t.Fatalf("finding.Kind = %q, want %q", finding.Kind, "supervisor_stale_binary")
	}
	if finding.Code != "D1081" {
		t.Fatalf("finding.Code = %q, want %q", finding.Code, "D1081")
	}
}

func TestFreshBinaryUnder30Minutes(t *testing.T) {
	supervisorStart := time.Date(2026, time.November, 13, 10, 20, 0, 0, time.UTC)
	binaryBuild := time.Date(2026, time.November, 13, 10, 30, 0, 0, time.UTC)

	_, ok := CheckStaleBinary(supervisorStart, binaryBuild)
	if ok {
		t.Fatal("expected no finding for a delta under 30 minutes")
	}
}

func TestEqualTimestampsNoFinding(t *testing.T) {
	supervisorStart := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC)
	binaryBuild := supervisorStart

	_, ok := CheckStaleBinary(supervisorStart, binaryBuild)
	if ok {
		t.Fatal("expected no finding when timestamps are equal")
	}
}
