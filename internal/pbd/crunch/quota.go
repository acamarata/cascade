// Package crunch implements quota enforcement.
package crunch

// Role identifiers for quota management and execution.
const (
	RoleA1 = "A1"
	RoleT0 = "T0"
	RoleA2 = "A2"
	RoleA3 = "A3"
	RoleA4 = "A4"
)

// Action identifiers for quota operations.
const (
	ActionManage  = "manage"
	ActionExecute = "execute"
)

// Scheduler enforces quota utilization caps based on roles.
type Scheduler struct{}

// Allow returns true if the role is permitted the action at the given utilization.
func (s Scheduler) Allow(role string, action string, utilization int) bool {
	switch role {
	case RoleA1, RoleT0:
		return action == ActionManage
	case RoleA2, RoleA3:
		return utilization <= 100
	case RoleA4:
		return utilization <= 50
	default:
		return false
	}
}
