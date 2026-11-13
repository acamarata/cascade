// Package farm provides capacity management primitives for farm nodes.
package farm

import (
	"errors"
	"time"
)

// Role represents the capacity role assigned to a farm node.
type Role string

// RolePrimary marks a node that participates only when demand exceeds
// baseline thresholds.
const RolePrimary Role = "primary"

// RoleAlwaysOn marks a node that stays active regardless of load.
const RoleAlwaysOn Role = "always-on"

// RoleOpen marks a node that accepts work without preconditions.
const RoleOpen Role = "open"

// Capacity holds the configured capacity parameters for a single farm node.
type Capacity struct {
	// Name is the human-readable identifier for the node.
	Name string
	// Role describes how the node participates in capacity decisions.
	Role Role
	// CapPct is the utilization ceiling expressed as a percentage.
	CapPct int
	// SwapFloorMB is the minimum swap usage in megabytes before the node
	// is considered constrained.
	SwapFloorMB int64
	// LoadMax is the maximum acceptable load average.
	LoadMax float64
	// MeasuredPct is the last observed utilization percentage.
	MeasuredPct float64
	// UpdatedAt records when MeasuredPct was last refreshed.
	UpdatedAt time.Time
}

// Decision represents the outcome of an admission check for a farm node.
type Decision struct {
	// Start indicates whether new work should be admitted to the node.
	Start bool
	// Reason explains why admission was granted or denied.
	Reason string
}

// Snapshot carries the current resource utilization of a farm node.
type Snapshot struct {
	// UsedPct is the current memory utilization as a percentage.
	UsedPct float64
	// SwapUsedMB is the current swap usage in megabytes.
	SwapUsedMB int64
	// Load is the current load average.
	Load float64
}

// Sample holds a single utilization observation for a farm node.
type Sample struct {
	// UsedPct is the observed memory utilization as a percentage.
	UsedPct float64
}

// ErrNotImplemented is returned by stub functions that have not yet been
// implemented.
var ErrNotImplemented = errors.New("not implemented")

// Admit evaluates whether a farm node should accept new work based on its
// capacity configuration and the current resource snapshot. The stub
// implementation always returns an empty Decision.
func Admit(row *Capacity, snap Snapshot) Decision {
	return Decision{}
}

// Record persists a utilization sample for a farm node. The stub
// implementation performs no action.
func Record(row *Capacity, s Sample) {
	return
}
