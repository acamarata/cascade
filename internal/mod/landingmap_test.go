package mod

import (
	"errors"
	"testing"
)

// mockResolver is an in-memory registry fixture implementing LandingResolver.
type mockResolver struct {
	data  map[string]string
	calls []string
}

func (m *mockResolver) Lookup(name string) (string, bool) {
	m.calls = append(m.calls, name)
	val, ok := m.data[name]
	return val, ok
}

func TestLandingResolvesToCrunchInModulesTable(t *testing.T) {
	table := &mockResolver{data: map[string]string{"Landing": "Crunch"}}
	mp := &mockResolver{data: map[string]string{"Landing": "Crunch"}}

	err := CheckLanding(table, mp)
	if err != nil {
		t.Fatalf("CheckLanding returned error: %v", err)
	}
	if len(table.calls) != 1 || table.calls[0] != "Landing" {
		t.Errorf("table Lookup called incorrectly: %v", table.calls)
	}
	if len(mp.calls) != 1 || mp.calls[0] != "Landing" {
		t.Errorf("map Lookup called incorrectly: %v", mp.calls)
	}

	// Check error cases for table conflicting
	errTbl := &mockResolver{data: map[string]string{"Landing": "Other"}}
	err = CheckLanding(errTbl, mp)
	if !errors.Is(err, ErrConflictingLanding) {
		t.Errorf("expected ErrConflictingLanding, got %v", err)
	}

	// Check missing case for table
	missingTbl := &mockResolver{data: map[string]string{}}
	err = CheckLanding(missingTbl, mp)
	if !errors.Is(err, ErrMissingLanding) {
		t.Errorf("expected ErrMissingLanding, got %v", err)
	}
}

func TestLandingResolvesToCrunchInModulesMap(t *testing.T) {
	table := &mockResolver{data: map[string]string{"Landing": "Crunch"}}
	mp := &mockResolver{data: map[string]string{"Landing": "Crunch"}}

	err := CheckLanding(table, mp)
	if err != nil {
		t.Fatalf("CheckLanding returned error: %v", err)
	}
	if len(table.calls) != 1 || table.calls[0] != "Landing" {
		t.Errorf("table Lookup called incorrectly: %v", table.calls)
	}
	if len(mp.calls) != 1 || mp.calls[0] != "Landing" {
		t.Errorf("map Lookup called incorrectly: %v", mp.calls)
	}

	// Check error cases for map conflicting
	errMp := &mockResolver{data: map[string]string{"Landing": "Other"}}
	err = CheckLanding(table, errMp)
	if !errors.Is(err, ErrConflictingLanding) {
		t.Errorf("expected ErrConflictingLanding, got %v", err)
	}

	// Check missing case for map
	missingMp := &mockResolver{data: map[string]string{}}
	err = CheckLanding(table, missingMp)
	if !errors.Is(err, ErrMissingLanding) {
		t.Errorf("expected ErrMissingLanding, got %v", err)
	}
}

func TestRegistriesAgreeOnLanding(t *testing.T) {
	table := &mockResolver{data: map[string]string{"Landing": "Crunch"}}
	mp := &mockResolver{data: map[string]string{"Landing": "Crunch"}}

	err := CheckLanding(table, mp)
	if err != nil {
		t.Fatalf("CheckLanding returned error: %v", err)
	}

	// allow a single valid fixture passed as both inputs
	single := &mockResolver{data: map[string]string{"Landing": "Crunch"}}
	err = CheckLanding(single, single)
	if err != nil {
		t.Fatalf("CheckLanding with single fixture returned error: %v", err)
	}
	if len(single.calls) != 2 {
		t.Errorf("expected 2 calls for single fixture, got %d", len(single.calls))
	}

	// disagreeing fixtures
	disagree := &mockResolver{data: map[string]string{"Landing": "NotCrunch"}}
	err = CheckLanding(table, disagree)
	if !errors.Is(err, ErrConflictingLanding) {
		t.Errorf("expected ErrConflictingLanding, got %v", err)
	}
}
