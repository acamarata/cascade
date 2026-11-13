// Package budget enforces resource limits for modules.
package budget

import "errors"

// Limits defines the maximum allowed resources for a module.
type Limits struct {
	MemBytes int64
	CPUMs    int64
	TimeMs   int64
}

// Usage represents the current resource consumption of a module.
type Usage Limits

// Alert records a budget violation.
type Alert struct {
	Module   string
	Resource string
	Used     int64
	Limit    int64
}

// Checker enforces resource budgets for modules.
type Checker struct {
	// Throttle is set to true when any module exceeds its budget.
	Throttle bool
	limits   map[string]Limits
	sink     func(Alert)
}

// ErrBudgetExceeded is returned when a module exceeds any of its resource limits.
var ErrBudgetExceeded = errors.New("budget exceeded")

// ErrUnknownModule is returned when a module has no defined budget.
var ErrUnknownModule = errors.New("unknown module")

// New creates a Checker with the given per-module limits and alert sink.
func New(limits map[string]Limits, sink func(Alert)) *Checker {
	return &Checker{
		limits: limits,
		sink:   sink,
	}
}

// Check verifies that the given module's usage is within its configured limits.
// It returns ErrUnknownModule if the module has no limits configured.
// It returns ErrBudgetExceeded, records an Alert, and sets Throttle=true if any limit is exceeded.
func (c *Checker) Check(module string, usage Usage) error {
	lim, ok := c.limits[module]
	if !ok {
		return ErrUnknownModule
	}

	var exceeded bool

	if usage.MemBytes > lim.MemBytes {
		if c.sink != nil {
			c.sink(Alert{Module: module, Resource: "memory", Used: usage.MemBytes, Limit: lim.MemBytes})
		}
		exceeded = true
	}
	if usage.CPUMs > lim.CPUMs {
		if c.sink != nil {
			c.sink(Alert{Module: module, Resource: "cpu", Used: usage.CPUMs, Limit: lim.CPUMs})
		}
		exceeded = true
	}
	if usage.TimeMs > lim.TimeMs {
		if c.sink != nil {
			c.sink(Alert{Module: module, Resource: "time", Used: usage.TimeMs, Limit: lim.TimeMs})
		}
		exceeded = true
	}

	if exceeded {
		c.Throttle = true
		return ErrBudgetExceeded
	}

	return nil
}
