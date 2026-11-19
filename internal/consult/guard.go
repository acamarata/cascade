// Package consult provides guard logic for model access control.
package consult

// Caller represents the identity making a request.
type Caller string

// Task represents the task identifier.
type Task string

// Model represents the model identifier.
type Model string

// GuardResult holds the outcome of a guard check.
type GuardResult struct {
	Allowed bool
	Code    string
}

const (
	// Owner is the authorized caller identity.
	Owner Caller = "owner"
	// T0 is the reserved task for owner communication.
	T0 Task = "T0"
	// A1 is the restricted model.
	A1 Model = "A1"
)

// CheckAccess determines whether the given caller may invoke the specified
// model for the given task. It returns a GuardResult indicating whether the
// request is allowed or rejected with an error code.
func CheckAccess(caller Caller, task Task, model Model) GuardResult {
	if model == A1 {
		if caller == Owner && task == T0 {
			return GuardResult{Allowed: true}
		}
		return GuardResult{Allowed: false, Code: "T0417"}
	}
	return GuardResult{Allowed: true}
}
