package mod

import (
	"testing"
)

// fakeSource implements Source for testing.
type fakeSource struct {
	plans map[string]PlanView
	list  []string
}

func (f *fakeSource) Plan(id string) (PlanView, error) {
	if p, ok := f.plans[id]; ok {
		return p, nil
	}
	return PlanView{}, ErrNoPlan
}

func (f *fakeSource) List() []string {
	return f.list
}

func TestPlanModelReadsViaSource(t *testing.T) {
	src := &fakeSource{
		plans: map[string]PlanView{
			"p1": {ID: "p1", Title: "Plan One", Steps: []string{"step1"}},
		},
		list: []string{"p1"},
	}
	pm := NewPlanModel(src)
	view, err := pm.Get("p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.ID != "p1" {
		t.Errorf("expected ID 'p1', got '%s'", view.ID)
	}
	if view.Title != "Plan One" {
		t.Errorf("expected Title 'Plan One', got '%s'", view.Title)
	}
	if len(view.Steps) != 1 || view.Steps[0] != "step1" {
		t.Errorf("unexpected Steps: %v", view.Steps)
	}
}

func TestPlanModelListsViaSource(t *testing.T) {
	src := &fakeSource{
		plans: map[string]PlanView{
			"p1": {ID: "p1"},
			"p2": {ID: "p2"},
		},
		list: []string{"p1", "p2"},
	}
	pm := NewPlanModel(src)
	got := pm.Plans()
	if len(got) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(got))
	}
	if got[0] != "p1" || got[1] != "p2" {
		t.Errorf("expected ['p1','p2'], got %v", got)
	}
}

func TestPlanModelUnknownPlan(t *testing.T) {
	src := &fakeSource{
		plans: map[string]PlanView{},
		list:  []string{},
	}
	pm := NewPlanModel(src)
	_, err := pm.Get("zz")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != ErrNoPlan {
		t.Errorf("expected ErrNoPlan, got %v", err)
	}
}

func TestPlanModelNilSource(t *testing.T) {
	pm := NewPlanModel(nil)
	_, err := pm.Get("p1")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != ErrNoSource {
		t.Errorf("expected ErrNoSource, got %v", err)
	}
}
