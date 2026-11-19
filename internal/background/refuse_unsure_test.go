package background

import "testing"

func TestRefuseUnsureState(t *testing.T) {
	d := RefuseUnsure("unsure", []string{"metrics"})
	if d.Decision != "refuse" {
		t.Errorf("expected decision refuse, got %s", d.Decision)
	}
	if d.Code != "M1888" {
		t.Errorf("expected code M1888, got %s", d.Code)
	}
	if d.Supervisor != "active" {
		t.Errorf("expected supervisor active, got %s", d.Supervisor)
	}
}

func TestRefuseMissingPanels(t *testing.T) {
	d := RefuseUnsure("ready", []string{})
	if d.Decision != "refuse" {
		t.Errorf("expected decision refuse, got %s", d.Decision)
	}
	if d.Code != "M1888" {
		t.Errorf("expected code M1888, got %s", d.Code)
	}
	if d.Supervisor != "active" {
		t.Errorf("expected supervisor active, got %s", d.Supervisor)
	}
}

func TestProceedKnownStateWithPanels(t *testing.T) {
	d := RefuseUnsure("ready", []string{"metrics"})
	if d.Decision != "proceed" {
		t.Errorf("expected decision proceed, got %s", d.Decision)
	}
	if d.Code != "" {
		t.Errorf("expected code empty, got %s", d.Code)
	}
	if d.Supervisor != "active" {
		t.Errorf("expected supervisor active, got %s", d.Supervisor)
	}
}

func TestFailClosedUnknownState(t *testing.T) {
	d := RefuseUnsure("", []string{"metrics"})
	if d.Decision != "refuse" {
		t.Errorf("expected decision refuse, got %s", d.Decision)
	}
	if d.Code != "M1888" {
		t.Errorf("expected code M1888, got %s", d.Code)
	}
	if d.Supervisor != "active" {
		t.Errorf("expected supervisor active, got %s", d.Supervisor)
	}
}
