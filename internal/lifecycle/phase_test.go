package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestPhaseConstantsExist verifies that the Phase type has ten constants
// matching Conductor Lifecycle names and that ErrIllegalTransition is non-nil.
func TestPhaseConstantsExist(t *testing.T) {
	phases := []Phase{
		Seeded,
		RTP,
		RTB,
		Planning,
		RTF,
		Forging,
		Building,
		RTD,
		Deploying,
		Finished,
	}

	if len(phases) != 10 {
		t.Fatalf("expected 10 phase constants, got %d", len(phases))
	}

	seen := make(map[Phase]bool)
	for _, p := range phases {
		if p == "" {
			t.Fatal("phase constant must not be empty")
		}
		if seen[p] {
			t.Fatalf("duplicate phase constant: %s", p)
		}
		seen[p] = true
	}

	if ErrIllegalTransition == nil {
		t.Fatal("ErrIllegalTransition must not be nil")
	}

	var err = ErrIllegalTransition
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatal("ErrIllegalTransition does not match itself via errors.Is")
	}
}

// TestEveryIllegalMoveIsRefused verifies that any transition not explicitly legal
// returns an error wrapping ErrIllegalTransition, while legal moves succeed.
func TestEveryIllegalMoveIsRefused(t *testing.T) {
	allPhases := []Phase{
		Seeded, RTP, RTB, Planning, RTF, Forging, Building, RTD, Deploying, Finished,
	}

	legal := map[Phase][]Phase{
		Seeded:    {RTP, RTB},
		RTP:       {Planning, RTB},
		Planning:  {RTF, RTP},
		RTF:       {Forging, RTP},
		Forging:   {RTB, RTF},
		RTB:       {Building, RTF},
		Building:  {RTD, RTB},
		RTD:       {Deploying},
		Deploying: {Finished, RTD},
	}

	for _, from := range allPhases {
		for _, to := range allPhases {
			isLegal := false
			for _, allowed := range legal[from] {
				if to == allowed {
					isLegal = true
					break
				}
			}
			if isLegal {
				if err := Transition(from, to); err != nil {
					t.Errorf("Transition(%s, %s) returned error: %v", from, to, err)
				}
			} else {
				err := Transition(from, to)
				if err == nil {
					t.Errorf("Transition(%s, %s) should be illegal, got nil", from, to)
				} else if !errors.Is(err, ErrIllegalTransition) {
					t.Errorf("Transition(%s, %s) error = %v, want wrapping ErrIllegalTransition", from, to, err)
				}
			}
		}
	}
}

// TestStageNamesMatchTheConductor verifies that the ten Phase constants
// exactly equal the Conductor Lifecycle stage names.
func TestStageNamesMatchTheConductor(t *testing.T) {
	tests := []struct {
		phase Phase
		name  string
	}{
		{Seeded, "Seeded"},
		{RTP, "RTP"},
		{RTB, "RTB"},
		{Planning, "Planning"},
		{RTF, "RTF"},
		{Forging, "Forging"},
		{Building, "Building"},
		{RTD, "RTD"},
		{Deploying, "Deploying"},
		{Finished, "Finished"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.phase) != tt.name {
				t.Errorf("Phase constant %q does not match expected Conductor Lifecycle stage name %q", tt.phase, tt.name)
			}
		})
	}
}

// TestForceLogsObserved verifies that Force logs at Info level with the
// "observed" prefix and returns nil.
func TestForceLogsObserved(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	err := Force(context.Background(), logger, RTB)
	if err != nil {
		t.Fatalf("Force returned error: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "observed") {
		t.Errorf("log output does not contain 'observed': %s", logOutput)
	}
}
