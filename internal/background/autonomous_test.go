package background

import (
	"errors"
	"testing"
)

// TestNonAutonomousModeUnaffected verifies that BoundaryModeNormal delegates
// the original request exactly once to the normal callback and returns its
// result and error unchanged. A nil normal callback must return
// ErrInvalidBoundaryRequest.
func TestNonAutonomousModeUnaffected(t *testing.T) {
	if BoundaryModeNormal != Mode("normal") {
		t.Fatalf("BoundaryModeNormal = %q, want %q", BoundaryModeNormal, "normal")
	}

	req := BoundaryRequest{Action: "do", Question: "why"}
	wantResult := BoundaryResult{
		Action:    "done",
		Route:     "r1",
		Decision:  "d1",
		Code:      "c1",
		Continue:  true,
		Cards:     2,
		Pending:   3,
		WaitOwner: false,
	}
	wantErr := errors.New("custom error")

	calls := 0
	normal := func(r BoundaryRequest) (BoundaryResult, error) {
		calls++
		if r != req {
			t.Errorf("normal received %+v, want %+v", r, req)
		}
		return wantResult, wantErr
	}

	gotResult, gotErr := HandleBoundary(BoundaryModeNormal, req, normal)
	if calls != 1 {
		t.Fatalf("normal called %d times, want 1", calls)
	}
	if gotResult != wantResult {
		t.Errorf("result = %+v, want %+v", gotResult, wantResult)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("err = %v, want %v", gotErr, wantErr)
	}

	_, err := HandleBoundary(BoundaryModeNormal, req, nil)
	if !errors.Is(err, ErrInvalidBoundaryRequest) {
		t.Errorf("nil normal err = %v, want %v", err, ErrInvalidBoundaryRequest)
	}

	checkRejectedModes(t, req, normal)
}

func checkRejectedModes(t *testing.T, req BoundaryRequest, normal BoundaryNormal) {
	t.Helper()
	cases := []struct {
		name string
		mode Mode
	}{
		{"unknown mode", Mode("unknown")},
		{"empty mode", Mode("")},
		{"interactive mode", ModeInteractive},
	}
	for _, c := range cases {
		_, err := HandleBoundary(c.mode, req, normal)
		if !errors.Is(err, ErrInvalidBoundaryRequest) {
			t.Errorf("%s err = %v, want %v", c.name, err, ErrInvalidBoundaryRequest)
		}
	}
}
