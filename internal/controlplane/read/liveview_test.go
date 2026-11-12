package read

import (
	"errors"
	"testing"
	"time"
)

// fakeStore implements Store for testing.
type fakeStore struct {
	record TicketRecord
	found  bool
	err    error
	calls  int
}

// Ticket returns the configured record, found flag, and error.
func (f *fakeStore) Ticket(_ string) (TicketRecord, bool, error) {
	f.calls++
	return f.record, f.found, f.err
}

type liveViewCase struct {
	name            string
	record          TicketRecord
	found           bool
	wantTimeInState time.Duration
	checkNilSlices  bool
}

func plainRecord(owner string, attempts int) TicketRecord {
	return TicketRecord{
		State:          "active",
		Stage:          "review",
		Level:          "L2",
		AIInstructions: "review carefully",
		Owner:          owner,
		Attempts:       attempts,
	}
}

func liveViewCases(now time.Time) []liveViewCase {
	baseRecord := plainRecord("alice", 2)
	baseRecord.Blockers = []Blocker{
		{Ticket: "T0", Kind: "dependency", Ref: "R0", CreatedAt: now.Add(-2 * time.Hour), Cleared: false},
		{Ticket: "T9", Kind: "manual", Ref: "R9", CreatedAt: now.Add(-1 * time.Hour), Cleared: true},
	}
	baseRecord.Dependencies = []string{"T2", "T3"}
	baseRecord.Events = []Event{
		{ID: "E1", Kind: "created", At: now.Add(-3 * time.Hour)},
		{ID: "E2", Kind: "updated", At: now.Add(-30 * time.Minute)},
	}
	baseRecord.StateEnteredAt = now.Add(-30 * time.Minute)

	return []liveViewCase{
		{"all fields populated", baseRecord, true, 30 * time.Minute, false},
		{"zero StateEnteredAt yields duration 0", plainRecord("alice", 2), true, 0, true},
		{"zero attempts", plainRecord("alice", 0), true, 0, true},
		{"unassigned owner", plainRecord("", 2), true, 0, true},
	}
}

func checkLiveViewScalars(t *testing.T, lv *LiveView, rec TicketRecord, want time.Duration) {
	t.Helper()
	if lv.State != rec.State {
		t.Errorf("State = %q, want %q", lv.State, rec.State)
	}
	if lv.Stage != rec.Stage {
		t.Errorf("Stage = %q, want %q", lv.Stage, rec.Stage)
	}
	if lv.Level != rec.Level {
		t.Errorf("Level = %q, want %q", lv.Level, rec.Level)
	}
	if lv.AIInstructions != rec.AIInstructions {
		t.Errorf("AIInstructions = %q, want %q", lv.AIInstructions, rec.AIInstructions)
	}
	if lv.Owner != rec.Owner {
		t.Errorf("Owner = %q, want %q", lv.Owner, rec.Owner)
	}
	if lv.Attempts != rec.Attempts {
		t.Errorf("Attempts = %d, want %d", lv.Attempts, rec.Attempts)
	}
	if lv.TimeInState != want {
		t.Errorf("TimeInState = %v, want %v", lv.TimeInState, want)
	}
}

func checkLiveViewSlices(t *testing.T, lv *LiveView, rec TicketRecord) {
	t.Helper()
	// Slices are copied (nonnil even when input is nil).
	if lv.Blockers == nil {
		t.Error("Blockers is nil, want nonnil slice")
	}
	if lv.Dependencies == nil {
		t.Error("Dependencies is nil, want nonnil slice")
	}
	if lv.Events == nil {
		t.Error("Events is nil, want nonnil slice")
	}

	// Slice contents match record.
	if len(lv.Blockers) != len(rec.Blockers) {
		t.Errorf("len(Blockers) = %d, want %d", len(lv.Blockers), len(rec.Blockers))
	} else {
		for i := range lv.Blockers {
			if lv.Blockers[i] != rec.Blockers[i] {
				t.Errorf("Blockers[%d] = %+v, want %+v", i, lv.Blockers[i], rec.Blockers[i])
			}
		}
	}
	if len(lv.Dependencies) != len(rec.Dependencies) {
		t.Errorf("len(Dependencies) = %d, want %d", len(lv.Dependencies), len(rec.Dependencies))
	} else {
		for i := range lv.Dependencies {
			if lv.Dependencies[i] != rec.Dependencies[i] {
				t.Errorf("Dependencies[%d] = %q, want %q", i, lv.Dependencies[i], rec.Dependencies[i])
			}
		}
	}
	if len(lv.Events) != len(rec.Events) {
		t.Errorf("len(Events) = %d, want %d", len(lv.Events), len(rec.Events))
	} else {
		for i := range lv.Events {
			if lv.Events[i] != rec.Events[i] {
				t.Errorf("Events[%d] = %+v, want %+v", i, lv.Events[i], rec.Events[i])
			}
		}
	}
}

func checkLiveViewNoAlias(t *testing.T, lv *LiveView, rec TicketRecord) {
	t.Helper()
	if len(lv.Blockers) > 0 {
		lv.Blockers[0].Ticket = "MUTATED"
		if len(rec.Blockers) > 0 && rec.Blockers[0].Ticket == "MUTATED" {
			t.Error("modifying lv.Blockers mutated store record")
		}
	}
	if len(lv.Dependencies) > 0 {
		lv.Dependencies[0] = "MUTATED"
		if len(rec.Dependencies) > 0 && rec.Dependencies[0] == "MUTATED" {
			t.Error("modifying lv.Dependencies mutated store record")
		}
	}
	if len(lv.Events) > 0 {
		lv.Events[0].ID = "MUTATED"
		if len(rec.Events) > 0 && rec.Events[0].ID == "MUTATED" {
			t.Error("modifying lv.Events mutated store record")
		}
	}
}

// TestLiveViewReturnsAllFields verifies that GetLiveView copies every field
// from the store record into the returned LiveView, handles edge cases
// (zero timestamp, zero attempts, unassigned owner, nil collections),
// and that modifying returned slices does not affect the store record.
func TestLiveViewReturnsAllFields(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	for _, tt := range liveViewCases(now) {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{record: tt.record, found: tt.found}

			lv, err := GetLiveView(store, "T1", now)
			if err != nil {
				t.Fatalf("GetLiveView returned error: %v", err)
			}
			if lv == nil {
				t.Fatal("GetLiveView returned nil view")
			}
			if store.calls != 1 {
				t.Errorf("Ticket called %d times, want 1", store.calls)
			}
			checkLiveViewScalars(t, lv, tt.record, tt.wantTimeInState)
			checkLiveViewSlices(t, lv, tt.record)
			checkLiveViewNoAlias(t, lv, tt.record)
		})
	}
}

// TestLiveViewValidation verifies that GetLiveView returns ErrInvalidLiveView
// for invalid inputs and ErrUnknownTicket when the store reports not found.
func TestLiveViewValidation(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		id      string
		store   Store
		now     time.Time
		wantErr error
	}{
		{"empty ID", "", &fakeStore{}, now, ErrInvalidLiveView},
		{"whitespace ID", "   ", &fakeStore{}, now, ErrInvalidLiveView},
		{"nil Store", "T1", nil, now, ErrInvalidLiveView},
		{"zero now", "T1", &fakeStore{}, time.Time{}, ErrInvalidLiveView},
		{"negative Attempts", "T1", &fakeStore{record: TicketRecord{Attempts: -1}, found: true}, now, ErrInvalidLiveView},
		{"StateEnteredAt after now", "T1", &fakeStore{record: TicketRecord{StateEnteredAt: now.Add(1 * time.Minute)}, found: true}, now, ErrInvalidLiveView},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lv, err := GetLiveView(tt.store, tt.id, tt.now)
			if lv != nil {
				t.Error("GetLiveView returned nonnil view")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("GetLiveView error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestLiveViewStoreError verifies that a store error is returned unwrapped.
func TestLiveViewStoreError(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	storeErr := errors.New("store failure")
	store := &fakeStore{err: storeErr}

	lv, err := GetLiveView(store, "T1", now)
	if lv != nil {
		t.Error("GetLiveView returned nonnil view")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("GetLiveView error = %v, want %v", err, storeErr)
	}
	if errors.Is(err, ErrInvalidLiveView) {
		t.Error("error should not wrap ErrInvalidLiveView")
	}
	if errors.Is(err, ErrUnknownTicket) {
		t.Error("error should not wrap ErrUnknownTicket")
	}
}

// TestLiveViewNotFound verifies that GetLiveView returns ErrUnknownTicket
// when the store reports the ticket was not found.
func TestLiveViewNotFound(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{found: false}

	lv, err := GetLiveView(store, "T1", now)
	if lv != nil {
		t.Error("GetLiveView returned nonnil view")
	}
	if !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("GetLiveView error = %v, want %v", err, ErrUnknownTicket)
	}
}
