package background

import (
	"testing"
	"time"
)

func TestWatchSilentWithinSevenDays(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0.Add(6*24*time.Hour + 23*time.Hour + 59*time.Minute)

	state := WatchState{
		FirstPass: t0,
		Findings: []Finding{
			{Resolved: false},
		},
	}

	actions := Evaluate(state, now)

	fileCount := 0
	recordCount := 0
	for _, a := range actions {
		switch a.Type {
		case File:
			fileCount++
		case Record:
			recordCount++
		}
	}

	if recordCount == 0 {
		t.Error("expected at least one Record action during silent window")
	}
	if fileCount != 0 {
		t.Errorf("expected zero File actions during silent window, got %d", fileCount)
	}
}

func TestWatchFilesAfterSevenDays(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0.Add(7 * 24 * time.Hour)

	state := WatchState{
		FirstPass: t0,
		Findings: []Finding{
			{Resolved: false, FiledAt: time.Time{}},
		},
	}

	actions := Evaluate(state, now)

	fileCount := 0
	for _, a := range actions {
		if a.Type == File {
			fileCount++
		}
	}

	if fileCount != 1 {
		t.Errorf("expected exactly one File action after 7 days, got %d", fileCount)
	}
}

func TestWatchRefilesAfter24h(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	filedAt := t0.Add(8 * 24 * time.Hour)
	now := filedAt.Add(24 * time.Hour)

	state := WatchState{
		FirstPass: t0,
		Findings: []Finding{
			{Resolved: false, FiledAt: filedAt},
		},
	}

	actions := Evaluate(state, now)

	refileCount := 0
	for _, a := range actions {
		if a.Type == Refile {
			refileCount++
		}
	}

	if refileCount != 1 {
		t.Errorf("expected exactly one Refile action after 24h, got %d", refileCount)
	}
}

func TestWatchNoRefileWhenResolved(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	filedAt := t0.Add(8 * 24 * time.Hour)
	now := filedAt.Add(48 * time.Hour)

	state := WatchState{
		FirstPass: t0,
		Findings: []Finding{
			{Resolved: true, FiledAt: filedAt},
		},
	}

	actions := Evaluate(state, now)

	refileCount := 0
	for _, a := range actions {
		if a.Type == Refile {
			refileCount++
		}
	}

	if refileCount != 0 {
		t.Errorf("expected no Refile action for resolved incident, got %d", refileCount)
	}
}

func TestWatchNoRefileBefore24h(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	filedAt := t0.Add(8 * 24 * time.Hour)
	now := filedAt.Add(23*time.Hour + 59*time.Minute)

	state := WatchState{
		FirstPass: t0,
		Findings: []Finding{
			{Resolved: false, FiledAt: filedAt},
		},
	}

	actions := Evaluate(state, now)

	refileCount := 0
	for _, a := range actions {
		if a.Type == Refile {
			refileCount++
		}
	}

	if refileCount != 0 {
		t.Errorf("expected no Refile action before 24h, got %d", refileCount)
	}
}
