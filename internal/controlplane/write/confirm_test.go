package write

import "testing"

// TestSupervisorDeadSingleTickNoFinding verifies that a single tick with
// supervisor absent does not fire a finding.
func TestSupervisorDeadSingleTickNoFinding(t *testing.T) {
	ticks := []Tick{
		{SupervisorAlive: false, StopMarker: false, Load1: 10.0},
	}
	findings := Confirm(ticks)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

// TestSupervisorDeadTwoTicksFiresD1079 verifies that two consecutive ticks
// with supervisor absent (without stop marker) fires D1079.
func TestSupervisorDeadTwoTicksFiresD1079(t *testing.T) {
	ticks := []Tick{
		{SupervisorAlive: false, StopMarker: false, Load1: 10.0},
		{SupervisorAlive: false, StopMarker: false, Load1: 10.0},
	}
	findings := Confirm(ticks)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Code != "D1079" {
		t.Errorf("expected code D1079, got %s", findings[0].Code)
	}
}

// TestLoadHighSingleSpikeNoFinding verifies that a single tick with load
// above 60 does not fire a finding.
func TestLoadHighSingleSpikeNoFinding(t *testing.T) {
	ticks := []Tick{
		{SupervisorAlive: true, StopMarker: false, Load1: 65.0},
	}
	findings := Confirm(ticks)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

// TestLoadHighTwoTicksAbove60FiresD1084 verifies that two consecutive ticks
// with load above 60 fires D1084.
func TestLoadHighTwoTicksAbove60FiresD1084(t *testing.T) {
	ticks := []Tick{
		{SupervisorAlive: true, StopMarker: false, Load1: 65.0},
		{SupervisorAlive: true, StopMarker: false, Load1: 65.0},
	}
	findings := Confirm(ticks)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Code != "D1084" {
		t.Errorf("expected code D1084, got %s", findings[0].Code)
	}
}

// TestSupervisorDeadWithStopMarkerNoFinding verifies that two ticks with
// supervisor absent but stop marker present does not fire a finding.
func TestSupervisorDeadWithStopMarkerNoFinding(t *testing.T) {
	ticks := []Tick{
		{SupervisorAlive: false, StopMarker: true, Load1: 10.0},
		{SupervisorAlive: false, StopMarker: true, Load1: 10.0},
	}
	findings := Confirm(ticks)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}
