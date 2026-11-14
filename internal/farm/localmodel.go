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
