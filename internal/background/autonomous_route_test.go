package background

import (
	"errors"
	"testing"
)

type mockSupervisor struct {
	answer string
	err    error
	calls  int
}

func (m *mockSupervisor) Answer(_ string) (string, error) {
	m.calls++
	return m.answer, m.err
}

// TestAutonomousSilentRoutesToSupervisor verifies that ModeAutonomous with
// guard autonomousSilent routes questions to the supervisor and that
// non-silent guards or nil supervisors return ErrSupervisorUnavailable.
func TestAutonomousSilentRoutesToSupervisor(t *testing.T) {
	t.Run("silent guard routes to supervisor", testAutonomousRouteSilentGuardRoutesToSupervisor)
	t.Run("non-silent guard returns ErrSupervisorUnavailable", testAutonomousRouteNonsilentGuardReturnsErrsupervisorunavailable)
	t.Run("nil supervisor returns ErrSupervisorUnavailable", testAutonomousRouteNilSupervisorReturnsErrsupervisorunavailable)
	t.Run("callback error wraps ErrBoundarySupervisor", testAutonomousRouteCallbackErrorWrapsErrboundarysupervisor)
	t.Run("empty question does not call supervisor", testAutonomousRouteEmptyQuestionDoesNotCallSupervisor)
	t.Run("boundary action replaced with noop and M1887", testAutonomousRouteBoundaryActionReplacedWithNoopAndM1887)
}

// testAutonomousRouteSilentGuardRoutesToSupervisor checks: silent guard routes to supervisor.
func testAutonomousRouteSilentGuardRoutesToSupervisor(t *testing.T) {
	sup := &mockSupervisor{answer: "approved"}
	req := BoundaryRequest{Action: "read", Question: "q"}
	res, err := HandleAutonomousRoute(ModeAutonomous, autonomousSilent, req, sup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "read" {
		t.Errorf("Action = %q, want %q", res.Action, "read")
	}
	if !res.Continue {
		t.Error("Continue = false, want true")
	}
	if res.Route != "supervisor" {
		t.Errorf("Route = %q, want %q", res.Route, "supervisor")
	}
	if res.Decision != "approved" {
		t.Errorf("Decision = %q, want %q", res.Decision, "approved")
	}
	if sup.calls != 1 {
		t.Errorf("Answer called %d times, want 1", sup.calls)
	}
	if res.Cards != 0 || res.Pending != 0 || res.WaitOwner {
		t.Error("cards, pending, or wait state created in autonomous mode")
	}
}

// testAutonomousRouteNonsilentGuardReturnsErrsupervisorunavailable checks: non-silent guard returns ErrSupervisorUnavailable.
func testAutonomousRouteNonsilentGuardReturnsErrsupervisorunavailable(t *testing.T) {
	sup := &mockSupervisor{answer: "x"}
	req := BoundaryRequest{Action: "read", Question: "q"}
	_, err := HandleAutonomousRoute(ModeAutonomous, guardNotSilent, req, sup)
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("err = %v, want ErrSupervisorUnavailable", err)
	}
	if sup.calls != 0 {
		t.Errorf("Answer called %d times, want 0", sup.calls)
	}
}

// testAutonomousRouteNilSupervisorReturnsErrsupervisorunavailable checks: nil supervisor returns ErrSupervisorUnavailable.
func testAutonomousRouteNilSupervisorReturnsErrsupervisorunavailable(t *testing.T) {
	req := BoundaryRequest{Action: "read", Question: "q"}
	_, err := HandleAutonomousRoute(ModeAutonomous, autonomousSilent, req, nil)
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("err = %v, want ErrSupervisorUnavailable", err)
	}
}

// testAutonomousRouteCallbackErrorWrapsErrboundarysupervisor checks: callback error wraps ErrBoundarySupervisor.
func testAutonomousRouteCallbackErrorWrapsErrboundarysupervisor(t *testing.T) {
	cause := errors.New("timeout")
	sup := &mockSupervisor{err: cause}
	req := BoundaryRequest{Action: "read", Question: "q"}
	_, err := HandleAutonomousRoute(ModeAutonomous, autonomousSilent, req, sup)
	if !errors.Is(err, ErrBoundarySupervisor) {
		t.Fatalf("err = %v, want ErrBoundarySupervisor", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want to wrap cause %v", err, cause)
	}
}

// testAutonomousRouteEmptyQuestionDoesNotCallSupervisor checks: empty question does not call supervisor.
func testAutonomousRouteEmptyQuestionDoesNotCallSupervisor(t *testing.T) {
	sup := &mockSupervisor{answer: "x"}
	req := BoundaryRequest{Action: "read", Question: ""}
	res, err := HandleAutonomousRoute(ModeAutonomous, autonomousSilent, req, sup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Route != "" {
		t.Errorf("Route = %q, want empty", res.Route)
	}
	if sup.calls != 0 {
		t.Errorf("Answer called %d times, want 0", sup.calls)
	}
}

// testAutonomousRouteBoundaryActionReplacedWithNoopAndM1887 checks: boundary action replaced with noop and M1887.
func testAutonomousRouteBoundaryActionReplacedWithNoopAndM1887(t *testing.T) {
	req := BoundaryRequest{Action: "boundary", Question: ""}
	res, err := HandleAutonomousRoute(ModeAutonomous, autonomousSilent, req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "noop" {
		t.Errorf("Action = %q, want %q", res.Action, "noop")
	}
	if res.Code != "M1887" {
		t.Errorf("Code = %q, want %q", res.Code, "M1887")
	}
	if !res.Continue {
		t.Error("Continue = false, want true")
	}
}
