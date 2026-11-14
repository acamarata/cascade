package hj

import "testing"

func TestExit3AfterModelCallReportsError(t *testing.T) {
	state := &ModelCallState{ModelCalls: 1}
	res := HandleExit3(3, state)

	if res.Action != ActionReportError {
		t.Errorf("expected ActionReportError, got %v", res.Action)
	}
	if res.SecondHostRun {
		t.Errorf("expected SecondHostRun=false, got true")
	}
	if res.Code != D3917 {
		t.Errorf("expected Code=%v, got %v", D3917, res.Code)
	}
}

func TestExit3BeforeModelCallHandsBack(t *testing.T) {
	state := &ModelCallState{ModelCalls: 0}
	res := HandleExit3(3, state)

	if res.Action != ActionHandBack {
		t.Errorf("expected ActionHandBack, got %v", res.Action)
	}
	if res.SecondHostRun {
		t.Errorf("expected SecondHostRun=false, got true")
	}
	if res.Code != D3917 {
		t.Errorf("expected Code=%v, got %v", D3917, res.Code)
	}
}

func TestExit3StateUnavailableFailsClosed(t *testing.T) {
	state := &ModelCallState{Unavailable: true}
	res := HandleExit3(3, state)

	if res.Action != ActionFailClosed {
		t.Errorf("expected ActionFailClosed, got %v", res.Action)
	}
	if res.SecondHostRun {
		t.Errorf("expected SecondHostRun=false, got true")
	}
	if res.Err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestNonExit3ReturnsUnhandled(t *testing.T) {
	state := &ModelCallState{ModelCalls: 1}
	res := HandleExit3(4, state)

	if res.Action != ActionUnhandled {
		t.Errorf("expected ActionUnhandled, got %v", res.Action)
	}
	if res.Err != nil {
		t.Errorf("expected no error, got %v", res.Err)
	}
}
