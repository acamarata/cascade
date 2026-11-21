// Package farm provides local model lifecycle utilities.
package farm

import "time"

// KeepWarm defines the policy for keeping a local model loaded in memory.
type KeepWarm struct {
	// IdleMinutes is the number of minutes a model can remain unused before
	// it becomes eligible for unloading. The default is 10, sourced from
	// configuration.
	IdleMinutes int
}

// ShouldUnload reports whether a model whose last use was at lastUse should
// be unloaded at time now, given the keep-warm policy.
func ShouldUnload(lastUse, now time.Time, policy KeepWarm) bool {
	idleDuration := time.Duration(policy.IdleMinutes) * time.Minute
	return now.Sub(lastUse) > idleDuration
}

// Task represents a request or operation to be routed.
type Task struct {
	// IsDecision indicates the task is a decision model call (short classifier).
	IsDecision bool
	// IsPrivateChat indicates the task is a privacy-bound chat session.
	IsPrivateChat bool
	// IsOffline indicates the task is running in an offline environment.
	IsOffline bool
}

// WantsLocal reports whether a task should be executed locally rather than
// being routed to a gateway or hosted model. It returns true for decision
// model calls, privacy-bound chats, and offline runs.
func WantsLocal(task Task) bool {
	return task.IsDecision || task.IsPrivateChat || task.IsOffline
}
