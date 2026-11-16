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

// invalidMetricsTestCase represents a test case for validating ramp metrics.
type invalidMetricsTestCase struct {
	name    string
	m       interface{}
	dataDir string
	u       interface{}
	now     time.Time
}

// invalidMetricsTests contains test cases for TestRampInvalidMetrics.
var invalidMetricsTests = []invalidMetricsTestCase{
	{
		name:    "Metrics NaN HangsA",
		m:       &Metrics{HangsA: math.NaN(), HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics infinity HangsB",
		m:       &Metrics{HangsA: 0, HangsB: math.Inf(1), Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics negative infinity Shedding",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: math.Inf(-1), Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics Shedding over 100",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 101, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics Shedding below 0",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: -1, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics BudgetUsed over 100",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 150},
		dataDir: "/tmp/data",
	},
	{
		name:    "Metrics negative Queues",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: -1, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "StageMetrics NaN Overloaded",
		m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: math.NaN(), FailureShare: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "StageMetrics infinity FailureShare",
		m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: math.Inf(1), BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "StageMetrics Overloaded over 100",
		m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 200, FailureShare: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "StageMetrics FailureShare below 0",
		m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: -0.5, BudgetUsed: 0},
		dataDir: "/tmp/data",
	},
	{
		name:    "StageMetrics BudgetUsed over 100",
		m:       &StageMetrics{WorkWaiting: false, StreamsUsed: 0, SlotsRunning: 0, HangsA: 0, HangsB: 0, HangLine: 0, Code429: false, Overloaded: 0, FailureShare: 0, BudgetUsed: 100.1},
		dataDir: "/tmp/data",
	},
	{
		name:    "empty dataDir",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "",
	},
	{
		name:    "nil UsageLog",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
		u:       nil,
	},
	{
		name:    "typed nil UsageLog",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
		u:       (*mockUsageLog)(nil),
	},
	{
		name:    "zero time",
		m:       &Metrics{HangsA: 0, HangsB: 0, Shedding: 0, Queues: 0, BudgetUsed: 0},
		dataDir: "/tmp/data",
		now:     time.Time{},
	},
}

func TestRampInvalidMetrics(t *testing.T) {
	now := time.Now()
	validLog := &mockUsageLog{}

	tests := make([]invalidMetricsTestCase, len(invalidMetricsTests))
	copy(tests, invalidMetricsTests)

	// Populate runtime-dependent fields
	for i := range tests {
		switch tests[i].name {
		case "nil UsageLog", "typed nil UsageLog":
			// Keep u as is
		case "zero time":
			tests[i].now = time.Time{}
		default:
			tests[i].u = validLog
			tests[i].now = now
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageLog := tt.u
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
