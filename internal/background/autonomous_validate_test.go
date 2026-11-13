package background

import (
	"errors"
	"testing"
)

// TestBoundaryActionReplacedWithSafestAlternative verifies that boundary actions
// are replaced with the safest alternative in autonomous mode.
func TestBoundaryActionReplacedWithSafestAlternative(t *testing.T) {
	boundaryActions := []string{"shell", "write", "network", "publish", "delete", "owner_prompt"}
	expected := BoundaryResult{Action: "noop", Code: "M1887", Continue: true}

	for _, action := range boundaryActions {
		t.Run(action, func(t *testing.T) {
			req := BoundaryRequest{Action: action}
			res, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != expected {
				t.Errorf("expected %+v, got %+v", expected, res)
			}
		})
	}
}

// TestBoundaryActionAllowedUnchanged verifies that non-boundary actions pass
// through unchanged in autonomous mode.
func TestBoundaryActionAllowedUnchanged(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		req := BoundaryRequest{Action: "read"}
		res, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != "read" {
			t.Errorf("expected action 'read', got '%s'", res.Action)
		}
	})

	t.Run("noop", func(t *testing.T) {
		req := BoundaryRequest{Action: "noop"}
		res, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != "noop" {
			t.Errorf("expected action 'noop', got '%s'", res.Action)
		}
	})
}

// TestBoundaryActionInvalidRequests verifies that invalid requests are rejected
// in autonomous mode.
func TestBoundaryActionInvalidRequests(t *testing.T) {
	t.Run("unknown_action", func(t *testing.T) {
		req := BoundaryRequest{Action: "unknown"}
		_, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
		if !errors.Is(err, ErrInvalidBoundaryRequest) {
			t.Errorf("expected ErrInvalidBoundaryRequest, got %v", err)
		}
	})

	t.Run("empty_action", func(t *testing.T) {
		req := BoundaryRequest{Action: ""}
		_, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
		if !errors.Is(err, ErrInvalidBoundaryRequest) {
			t.Errorf("expected ErrInvalidBoundaryRequest, got %v", err)
		}
	})

	t.Run("whitespace_only_question", func(t *testing.T) {
		req := BoundaryRequest{Action: "read", Question: "   "}
		_, err := HandleAutonomous(ModeAutonomous, autonomousSilent, req, nil, nil)
		if !errors.Is(err, ErrInvalidBoundaryRequest) {
			t.Errorf("expected ErrInvalidBoundaryRequest, got %v", err)
		}
	})
}
