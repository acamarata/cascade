package write

import "testing"

func TestRestartKeyReturnsNeedsRestart(t *testing.T) {
	res := ApplyChange([]string{"orch.listen_addr"})
	if res.Status != "needs_restart" {
		t.Fatalf("ApplyChange for restart-required key = %q, want %q", res.Status, "needs_restart")
	}
}

func TestLiveKeyDoesNotNeedRestart(t *testing.T) {
	res := ApplyChange([]string{"orch.log_level"})
	if res.Status != "applied" {
		t.Fatalf("ApplyChange for live key = %q, want %q", res.Status, "applied")
	}
}

func TestMixedChangeNeedsRestart(t *testing.T) {
	res := ApplyChange([]string{"orch.log_level", "orch.listen_addr"})
	if res.Status != "needs_restart" {
		t.Fatalf("ApplyChange for mixed keys = %q, want %q", res.Status, "needs_restart")
	}
}

func TestUnknownKeyFailsClosed(t *testing.T) {
	res := ApplyChange([]string{"orch.unknown_key"})
	if res.Status != "needs_restart" {
		t.Fatalf("ApplyChange for unknown key = %q, want %q", res.Status, "needs_restart")
	}
}
