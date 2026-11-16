package conductor

// RunState represents the current state of a CI run for stall detection.
type RunState struct {
	// RunID is the unique identifier of the run.
	RunID string
	// RefusedCount is the number of times the run has been refused.
	RefusedCount int
	// BlockedDependents is the number of dependents blocked on this run.
	BlockedDependents int
	// GuardBuildChanged indicates if the guard build has changed.
	GuardBuildChanged bool
	// CIPassed indicates if the CI has passed.
	CIPassed bool
	// IntegrationBranch is the branch being integrated.
	IntegrationBranch string
}

// StallClass represents the classification of a stall.
type StallClass int

const (
	// StallClassStalled indicates a generic stall.
	StallClassStalled StallClass = iota
	// StallClassRefused indicates the run was refused.
	StallClassRefused
	// StallClassBlocked indicates the run is blocked by dependencies.
	StallClassBlocked
	// StallClassRedCI indicates the CI is failing.
	StallClassRedCI
)

// HunterAction represents the action to be taken by the hunter.
type HunterAction int

const (
	// ActionRelaunch indicates the run should be relaunched.
	ActionRelaunch HunterAction = iota
	// ActionRequeue indicates the run should be requeued.
	ActionRequeue
	// ActionMessage indicates a message should be sent.
	ActionMessage
	// ActionRaiseWidth indicates the concurrency width should be raised.
	ActionRaiseWidth
)

// StallDetector detects stalls based on the run state.
type StallDetector interface {
	// Detect analyzes the run state and returns the stall classification and whether a stall was detected.
	Detect(state RunState) (StallClass, bool)
}
