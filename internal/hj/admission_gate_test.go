package hj

import "testing"

func TestAdmitAllThreeHaveRoom(t *testing.T) {
	ticket := Ticket{
		ModelReqs:   1,
		CPU:         2,
		RAM:         4,
		VerifySlots: 1,
	}
	budgets := Budgets{
		Model:  5,
		CPU:    4,
		RAM:    8,
		Verify: 2,
	}
	ok, reason := Admit(ticket, budgets)
	if !ok {
		t.Fatalf("expected true, got false with reason %q", reason)
	}
	if reason != "" {
		t.Fatalf("expected empty reason, got %q", reason)
	}
}

func TestRejectModelBudgetExhausted(t *testing.T) {
	ticket := Ticket{
		ModelReqs:   1,
		CPU:         2,
		RAM:         4,
		VerifySlots: 1,
	}
	budgets := Budgets{
		Model:  0,
		CPU:    4,
		RAM:    8,
		Verify: 2,
	}
	ok, reason := Admit(ticket, budgets)
	if ok {
		t.Fatal("expected false, got true")
	}
	if reason != "model" {
		t.Fatalf("expected reason 'model', got %q", reason)
	}
}

func TestRejectExecBudgetExhausted(t *testing.T) {
	ticket := Ticket{
		ModelReqs:   1,
		CPU:         2,
		RAM:         4,
		VerifySlots: 1,
	}
	budgets := Budgets{
		Model:  5,
		CPU:    4,
		RAM:    1,
		Verify: 2,
	}
	ok, reason := Admit(ticket, budgets)
	if ok {
		t.Fatal("expected false, got true")
	}
	if reason != "exec" {
		t.Fatalf("expected reason 'exec', got %q", reason)
	}
}

func TestRejectVerifyCapacityExhausted(t *testing.T) {
	ticket := Ticket{
		ModelReqs:   1,
		CPU:         2,
		RAM:         4,
		VerifySlots: 1,
	}
	budgets := Budgets{
		Model:  5,
		CPU:    4,
		RAM:    8,
		Verify: 0,
	}
	ok, reason := Admit(ticket, budgets)
	if ok {
		t.Fatal("expected false, got true")
	}
	if reason != "verify" {
		t.Fatalf("expected reason 'verify', got %q", reason)
	}
}
