package workflows

import (
	"context"
	"testing"
	"time"
)

// mockGateRunner records whether Vet or Test was called.
type mockGateRunner struct {
	vetCalled  bool
	testCalled bool
}

func (m *mockGateRunner) Vet(_ context.Context) error {
	m.vetCalled = true
	return nil
}

func (m *mockGateRunner) Test(_ context.Context) error {
	m.testCalled = true
	return nil
}

// harvestGateCase holds a single table-driven test case for HarvestGate.
type harvestGateCase struct {
	name   string
	ctx    context.Context
	in     HarvestInput
	runner GateRunner
}

// validHarvestInput returns a baseline valid HarvestInput for tests.
func validHarvestInput() HarvestInput {
	return HarvestInput{
		Greens:        1,
		SinceLast:     time.Second,
		Regenerations: 1,
		Sensitive:     false,
		Approvals:     []string{"alice"},
	}
}

// invalidHarvestCases defines the table cases for TestHarvestGateInvalidInput.
var invalidHarvestCases = []harvestGateCase{
	{
		name:   "nil context",
		ctx:    nil,
		in:     validHarvestInput(),
		runner: &mockGateRunner{},
	},
	{
		name:   "nil runner",
		ctx:    context.Background(),
		in:     validHarvestInput(),
		runner: nil,
	},
	{
		name: "negative greens",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        -1,
			SinceLast:     time.Second,
			Regenerations: 1,
			Approvals:     []string{"alice"},
		},
		runner: &mockGateRunner{},
	},
	{
		name: "negative regenerations",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        1,
			SinceLast:     time.Second,
			Regenerations: -1,
			Approvals:     []string{"alice"},
		},
		runner: &mockGateRunner{},
	},
	{
		name: "negative duration",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        1,
			SinceLast:     -time.Second,
			Regenerations: 1,
			Approvals:     []string{"alice"},
		},
		runner: &mockGateRunner{},
	},
	{
		name: "blank approval identity",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        1,
			SinceLast:     time.Second,
			Regenerations: 1,
			Approvals:     []string{""},
		},
		runner: &mockGateRunner{},
	},
	{
		name: "whitespace-only approval identity",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        1,
			SinceLast:     time.Second,
			Regenerations: 1,
			Approvals:     []string{"   "},
		},
		runner: &mockGateRunner{},
	},
	{
		name: "duplicate approval identities",
		ctx:  context.Background(),
		in: HarvestInput{
			Greens:        1,
			SinceLast:     time.Second,
			Regenerations: 1,
			Approvals:     []string{"alice", "alice"},
		},
		runner: &mockGateRunner{},
	},
}

// runInvalidCase executes a single invalid-input subtest and checks assertions.
func runInvalidCase(t *testing.T, tt harvestGateCase) {
	t.Helper()
	mock := &mockGateRunner{}
	runner := tt.runner
	if runner != nil {
		runner = mock
	}

	got, err := HarvestGate(tt.ctx, tt.in, runner)

	if got != false {
		t.Errorf("HarvestGate() returned %v, want false", got)
	}
	if err != ErrHarvestBlocked {
		t.Errorf("HarvestGate() error = %v, want ErrHarvestBlocked", err)
	}
	if mock.vetCalled || mock.testCalled {
		t.Error("HarvestGate() called runner methods; expected no callbacks")
	}
}

// TestHarvestGateInvalidInput verifies that invalid inputs return false and ErrHarvestBlocked
// without invoking any callbacks on the runner.
func TestHarvestGateInvalidInput(t *testing.T) {
	for _, tt := range invalidHarvestCases {
		t.Run(tt.name, func(t *testing.T) {
			runInvalidCase(t, tt)
		})
	}
}
