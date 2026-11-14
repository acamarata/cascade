// Package crunch implements quota enforcement.
package crunch

// Role identifiers for quota management and execution.
const (
	RoleA1    = "A1"
	RoleT0    = "T0"
	RoleAcctB = "acct-b"
	RoleAcctC = "acct-c"
	RoleAcctD = "acct-d"
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
	case RoleAcctB, RoleAcctC:
		return utilization <= 100
	case RoleAcctD:
		return utilization <= 50
	default:
		return false
	}
}
