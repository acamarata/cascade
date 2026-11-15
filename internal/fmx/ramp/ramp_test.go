package ramp

import (
	"errors"
	"math"
	"testing"
	"time"
)

// mockUsageLog is a mock implementation of fmx.UsageLog for testing.
type mockUsageLog struct {
	rows []string
}

func (m *mockUsageLog) Append(row string) {
	m.rows = append(m.rows, row)
}

func TestRampInvalidMetrics(t *testing.T) {
	now := time.Now()
	validLog := &mockUsageLog{}

	tests := []struct {
		name    string
		m       interface{}
		dataDir string
		u       interface{}
		now     time.Time
	}{
		{
			name:    "Metrics NaN HangsA",
			m:       &Metrics{HangsA: math.NaN(), HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics infinity HangsB",
			m:       &Metrics{HangsA: 0, HangsB: math.Inf(1), Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics negative infinity Shedding",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: math.Inf(-1), Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics Shedding over 100",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 101, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics Shedding below 0",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: -1, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics BudgetUsed over 100",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 150},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "Metrics negative Queues",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: -1, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "StageMetrics NaN Overloaded",
			m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: math.NaN(), FailureShare: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "StageMetrics infinity FailureShare",
			m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: math.Inf(1), BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "StageMetrics Overloaded over 100",
			m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 200, FailureShare: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "StageMetrics FailureShare below 0",
			m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: -0.5, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "StageMetrics BudgetUsed over 100",
			m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: 0, BudgetUsed: 100.1},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     now,
		},
		{
			name:    "empty dataDir",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "",
			u:       validLog,
			now:     now,
		},
		{
			name:    "nil UsageLog",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       nil,
			now:     now,
		},
		{
			name:    "typed nil UsageLog",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       (*mockUsageLog)(nil),
			now:     now,
		},
		{
			name:    "zero time",
			m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
			dataDir: "/tmp/data",
			u:       validLog,
			now:     time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usageLog interface{} = tt.u
			err := validate(tt.m, tt.dataDir, usageLog, tt.now)
			if err == nil {
				t.Errorf("expected error, got nil")
				return
			}
			if !errors.Is(err, ErrInvalidRampMetrics) {
				t.Errorf("expected ErrInvalidRampMetrics, got %v", err)
			}
			// Verify no disk or usage mutation
			if log, ok := tt.u.(*mockUsageLog); ok && log != nil {
				if len(log.rows) > 0 {
					t.Errorf("usage log was mutated: %v", log.rows)
				}
			}
		})
	}
}
