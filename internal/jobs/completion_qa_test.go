package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEvidenceKindsForGateSetQA pins QA to High and Critical gate sets.
func TestEvidenceKindsForGateSetQA(t *testing.T) {
	for _, tc := range []struct {
		class RiskClass
		want  int
	}{
		{RiskClassLow, 0},
		{RiskClassNormal, 0},
		{RiskClassHigh, 1},
		{RiskClassCritical, 1},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			gates, err := GateSetForRiskClass(tc.class)
			if err != nil {
				t.Fatal(err)
			}
			// Repeated gates must still require only one QA row.
			gates = append(gates, gates...)
			count := 0
			for _, kind := range evidenceKindsForGateSet(gates) {
				if kind == EvidenceQA {
					count++
				}
			}
			if count != tc.want {
				t.Fatalf("QA count = %d, want %d", count, tc.want)
			}
		})
	}
}

// TestEvidenceQAProducerIdentity checks identity refusals before ledger writes.
func TestEvidenceQAProducerIdentity(t *testing.T) {
	l := newQALedgerFixture(t)
	ctx := context.Background()
	for _, identity := range []string{"node:n-1", "approval:a-1", ""} {
		rec := passRecord(EvidenceQA, "qa-denied-"+identity)
		rec.AttestorIdentity = identity
		_, err := l.Append(ctx, rec, baseAuth())
		cause := errors.Unwrap(err)
		if cause != ErrEvidenceProducerDenied || cause.Error() != "permission-denied: jobs: evidence producer denied" {
			t.Fatalf("Append(%q) = %v, want exact ErrEvidenceProducerDenied sentinel and message", identity, err)
		}
		if !strings.Contains(err.Error(), `evidence kind "qa" requires an attestor_identity prefixed "daemon:"`) {
			t.Fatalf("Append(%q) error = %q, want QA daemon identity explanation", identity, err)
		}
		rows, queryErr := l.QueryAll(ctx, "job-1")
		if queryErr != ErrNoEvidence || queryErr.Error() != "not-found: jobs: no evidence record for that kind" || len(rows) != 0 {
			t.Fatalf("rows after denied Append(%q) = %v, %v", identity, rows, queryErr)
		}
	}
	rec, err := l.Append(ctx, passRecord(EvidenceQA, "qa-accepted"), baseAuth())
	if err != nil || rec.Seq != 1 || rec.AttestorIdentity != "daemon:d-1" {
		t.Fatalf("daemon QA Append = %+v, %v", rec, err)
	}
	rows, err := l.QueryAll(ctx, "job-1")
	if err != nil || len(rows) != 1 || rows[0].Kind != EvidenceQA || rows[0].Seq != rec.Seq {
		t.Fatalf("rows after daemon QA Append = %v, %v", rows, err)
	}
}

func newQALedgerFixture(t *testing.T) *EvidenceLedger {
	t.Helper()
	operatorHome := os.Getenv("HOME")
	home := t.TempDir()
	if qaHomeIsolated(operatorHome, home) {
		t.Fatal("isolation guard accepted operator HOME")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
		if !qaHomeIsolated(os.Getenv(key), home) {
			t.Fatalf("isolation guard refused temporary %s", key)
		}
	}
	l, _, _ := newLedgerFixture(t, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	return l
}

func qaHomeIsolated(home, temporaryHome string) bool {
	return home != "" && home == temporaryHome
}
