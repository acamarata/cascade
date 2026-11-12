package hygiene

import "testing"

func TestCanDropLogsAllMet(t *testing.T) {
	ok, reason := CanDropLogs(LearnedNote{Gaps: 0, Pending: false, InDigest: true, QueueLen: 0})
	if !ok {
		t.Fatalf("CanDropLogs = %v, %q; want true, \"\"", ok, reason)
	}
	if reason != "" {
		t.Fatalf("CanDropLogs reason = %q; want \"\"", reason)
	}
}

func TestCanDropLogsHasGaps(t *testing.T) {
	ok, reason := CanDropLogs(LearnedNote{Gaps: 1, Pending: false, InDigest: true, QueueLen: 0})
	if ok {
		t.Fatalf("CanDropLogs = %v, %q; want false, \"gaps\"", ok, reason)
	}
	if reason != "gaps" {
		t.Fatalf("CanDropLogs reason = %q; want \"gaps\"", reason)
	}
}

func TestCanDropLogsPending(t *testing.T) {
	ok, reason := CanDropLogs(LearnedNote{Gaps: 0, Pending: true, InDigest: true, QueueLen: 0})
	if ok {
		t.Fatalf("CanDropLogs = %v, %q; want false, \"pending\"", ok, reason)
	}
	if reason != "pending" {
		t.Fatalf("CanDropLogs reason = %q; want \"pending\"", reason)
	}
}

func TestCanDropLogsSummaryNotInDigest(t *testing.T) {
	ok, reason := CanDropLogs(LearnedNote{Gaps: 0, Pending: false, InDigest: false, QueueLen: 0})
	if ok {
		t.Fatalf("CanDropLogs = %v, %q; want false, \"digest\"", ok, reason)
	}
	if reason != "digest" {
		t.Fatalf("CanDropLogs reason = %q; want \"digest\"", reason)
	}
}

func TestCanDropLogsQueueNotEmpty(t *testing.T) {
	ok, reason := CanDropLogs(LearnedNote{Gaps: 0, Pending: false, InDigest: true, QueueLen: 2})
	if ok {
		t.Fatalf("CanDropLogs = %v, %q; want false, \"queue\"", ok, reason)
	}
	if reason != "queue" {
		t.Fatalf("CanDropLogs reason = %q; want \"queue\"", reason)
	}
}
