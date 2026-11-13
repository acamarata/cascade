package mod

import "errors"

// ErrNoPlan is returned when a requested plan does not exist.
var ErrNoPlan = errors.New("plan not found")

// ErrNoSource is returned when an operation is attempted on a PlanModel with no source.
var ErrNoSource = errors.New("no source configured")

// PlanView represents a read-only view of a plan's metadata and steps.
type PlanView struct {
	ID    string
	Title string
	Steps []string
}

// Source provides read access to plan data.
type Source interface {
	Plan(id string) (PlanView, error)
	List() []string
}

// PlanModel binds UI components to a Source. It holds no direct store handle.
type PlanModel struct {
	src Source
}

// NewPlanModel creates a new PlanModel bound to the given Source.
func NewPlanModel(src Source) *PlanModel {
	return &PlanModel{src: src}
}

// Get retrieves a plan by ID. Returns ErrNoSource if the model has no source.
// If the source returns ErrNoPlan, it is passed through unchanged.
func (m *PlanModel) Get(id string) (PlanView, error) {
	if m.src == nil {
		return PlanView{}, ErrNoSource
	}
	return m.src.Plan(id)
}

// Plans returns the list of all plan IDs. Returns nil if the model has no source.
func (m *PlanModel) Plans() []string {
	if m.src == nil {
		return nil
	}
	return m.src.List()
}
