package graph

import (
	"testing"
	"time"
)

func TestVerdictFreshWithin60s(t *testing.T) {
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	reportedAt := now.Add(-30 * time.Second)

	if !VerdictFresh(reportedAt, now) {
		t.Fatalf("VerdictFresh with a 30s old verdict = false, want true")
	}
}

func TestVerdictFreshAtBoundary(t *testing.T) {
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	reportedAt := now.Add(-60 * time.Second)

	if !VerdictFresh(reportedAt, now) {
		t.Fatalf("VerdictFresh with a 60s old verdict = false, want true")
	}
}

func TestVerdictStaleBeyond60s(t *testing.T) {
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	reportedAt := now.Add(-61 * time.Second)

	if VerdictFresh(reportedAt, now) {
		t.Fatalf("VerdictFresh with a 61s old verdict = true, want false")
	}
}
