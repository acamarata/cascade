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

func (m *mockGateRunner) Vet(ctx context.Context) error {
	m.vetCalled = true
	return nil
}

func (m *mockGateRunner) Test(ctx context.Context) error {
	m.testCalled = true
	return nil
}

// TestHarvestGateInvalidInput verifies that invalid inputs return false and ErrHarvestBlocked
// without invoking any callbacks on the runner.
func TestHarvestGateInvalidInput(t *testing.T) {
	validCtx := context.Background()
	validRunner := &mockGateRunner{}
	validInput := HarvestInput{
		Greens:        1,
		SinceLast:     time.Second,
		Regenerations: 1,
		Sensitive:     false,
		Approvals:     []string{"alice"},
	}

	tests := []struct {
		name   string
		ctx    context.Context
		in     HarvestInput
		runner GateRunner
	}{
		{
			name:   "nil context",
			ctx:    nil,
			in:     validInput,
			runner: validRunner,
		},
		{
			name:   "nil runner",
			ctx:    validCtx,
			in:     validInput,
			runner: nil,
		},
		{
			name: "negative greens",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        -1,
				SinceLast:     time.Second,
				Regenerations: 1,
				Approvals:     []string{"alice"},
			},
			runner: validRunner,
		},
		{
			name: "negative regenerations",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        1,
				SinceLast:     time.Second,
				Regenerations: -1,
				Approvals:     []string{"alice"},
			},
			runner: validRunner,
		},
		{
			name: "negative duration",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        1,
				SinceLast:     -time.Second,
				Regenerations: 1,
				Approvals:     []string{"alice"},
			},
			runner: validRunner,
		},
		{
			name: "blank approval identity",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        1,
				SinceLast:     time.Second,
				Regenerations: 1,
				Approvals:     []string{""},
			},
			runner: validRunner,
		},
		{
			name: "whitespace-only approval identity",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        1,
				SinceLast:     time.Second,
				Regenerations: 1,
				Approvals:     []string{"   "},
			},
			runner: validRunner,
		},
		{
			name: "duplicate approval identities",
			ctx:  validCtx,
			in: HarvestInput{
				Greens:        1,
				SinceLast:     time.Second,
				Regenerations: 1,
				Approvals:     []string{"alice", "alice"},
			},
			runner: validRunner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}
