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
func (f *fakeStore) Ticket(id string) (TicketRecord, bool, error) {
	f.calls++
	return f.record, f.found, f.err
}

// TestLiveViewReturnsAllFields verifies that GetLiveView copies every field
// from the store record into the returned LiveView, handles edge cases
// (zero timestamp, zero attempts, unassigned owner, nil collections),
// and that modifying returned slices does not affect the store record.
func TestLiveViewReturnsAllFields(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	// Base record with all fields populated.
	baseRecord := TicketRecord{
		State:          "active",
		Stage:          "review",
		Level:          "L2",
		AIInstructions: "review carefully",
		Owner:          "alice",
		Blockers: []Blocker{
			{Ticket: "T0", Kind: "dependency", Ref: "R0", CreatedAt: now.Add(-2 * time.Hour), Cleared: false},
			{Ticket: "T9", Kind: "manual", Ref: "R9", CreatedAt: now.Add(-1 * time.Hour), Cleared: true},
		},
		Dependencies: []string{"T2", "T3"},
		Attempts:     2,
		Events: []Event{
			{ID: "E1", Kind: "created", At: now.Add(-3 * time.Hour)},
			{ID: "E2", Kind: "updated", At: now.Add(-30 * time.Minute)},
		},
		StateEnteredAt: now.Add(-30 * time.Minute),
	}

	tests := []struct {
		name            string
		record          TicketRecord
		found           bool
		wantTimeInState time.Duration
		checkNilSlices  bool
	}{
		{
			name:            "all fields populated",
			record:          baseRecord,
			found:           true,
			wantTimeInState: 30 * time.Minute,
			checkNilSlices:  false,
		},
		{
			name: "zero StateEnteredAt yields duration 0",
			record: TicketRecord{
				State:          "active",
				Stage:          "review",
				Level:          "L2",
				AIInstructions: "review carefully",
				Owner:          "alice",
				Attempts:       2,
			},
			found:           true,
			wantTimeInState: 0,
			checkNilSlices:  true,
		},
		{
			name: "zero attempts",
			record: TicketRecord{
				State:          "active",
				Stage:          "review",
				Level:          "L2",
				AIInstructions: "review carefully",
				Owner:          "alice",
				Attempts:       0,
			},
			found:           true,
			wantTimeInState: 0,
			checkNilSlices:  true,
		},
		{
			name: "unassigned owner",
			record: TicketRecord{
				State:          "active",
				Stage:          "review",
				Level:          "L2",
				AIInstructions: "review carefully",
				Owner:          "",
				Attempts:       2,
			},
			found:           true,
			wantTimeInState: 0,
			checkNilSlices:  true,
		},
	}

	for _, tt := range tests {
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

			// Check scalar fields.
			if lv.State != tt.record.State {
				t.Errorf("State = %q, want %q", lv.State, tt.record.State)
			}
			if lv.Stage != tt.record.Stage {
				t.Errorf("Stage = %q, want %q", lv.Stage, tt.record.Stage)
			}
			if lv.Level != tt.record.Level {
				t.Errorf("Level = %q, want %q", lv.Level, tt.record.Level)
			}
			if lv.AIInstructions != tt.record.AIInstructions {
				t.Errorf("AIInstructions = %q, want %q", lv.AIInstructions, tt.record.AIInstructions)
			}
			if lv.Owner != tt.record.Owner {
				t.Errorf("Owner = %q, want %q", lv.Owner, tt.record.Owner)
			}
			if lv.Attempts != tt.record.Attempts {
				t.Errorf("Attempts = %d, want %d", lv.Attempts, tt.record.Attempts)
			}
			if lv.TimeInState != tt.wantTimeInState {
				t.Errorf("TimeInState = %v, want %v", lv.TimeInState, tt.wantTimeInState)
			}

			// Check slices are copied (nonnil even when input is nil).
			if lv.Blockers == nil {
				t.Error("Blockers is nil, want nonnil slice")
			}
			if lv.Dependencies == nil {
				t.Error("Dependencies is nil, want nonnil slice")
			}
			if lv.Events == nil {
				t.Error("Events is nil, want nonnil slice")
			}

			// Check slice contents match record.
			if len(lv.Blockers) != len(tt.record.Blockers) {
				t.Errorf("len(Blockers) = %d, want %d", len(lv.Blockers), len(tt.record.Blockers))
			} else {
				for i := range lv.Blockers {
					if lv.Blockers[i] != tt.record.Blockers[i] {
						t.Errorf("Blockers[%d] = %+v, want %+v", i, lv.Blockers[i], tt.record.Blockers[i])
					}
				}
			}
			if len(lv.Dependencies) != len(tt.record.Dependencies) {
				t.Errorf("len(Dependencies) = %d, want %d", len(lv.Dependencies), len(tt.record.Dependencies))
			} else {
				for i := range lv.Dependencies {
					if lv.Dependencies[i] != tt.record.Dependencies[i] {
						t.Errorf("Dependencies[%d] = %q, want %q", i, lv.Dependencies[i], tt.record.Dependencies[i])
					}
				}
			}
			if len(lv.Events) != len(tt.record.Events) {
				t.Errorf("len(Events) = %d, want %d", len(lv.Events), len(tt.record.Events))
			} else {
				for i := range lv.Events {
					if lv.Events[i] != tt.record.Events[i] {
						t.Errorf("Events[%d] = %+v, want %+v", i, lv.Events[i], tt.record.Events[i])
					}
				}
			}

			// Verify that modifying returned slices does not affect the store record.
			if len(lv.Blockers) > 0 {
				lv.Blockers[0].Ticket = "MUTATED"
				if len(tt.record.Blockers) > 0 && tt.record.Blockers[0].Ticket == "MUTATED" {
					t.Error("modifying lv.Blockers mutated store record")
				}
			}
			if len(lv.Dependencies) > 0 {
				lv.Dependencies[0] = "MUTATED"
				if len(tt.record.Dependencies) > 0 && tt.record.Dependencies[0] == "MUTATED" {
					t.Error("modifying lv.Dependencies mutated store record")
				}
			}
			if len(lv.Events) > 0 {
				lv.Events[0].ID = "MUTATED"
				if len(tt.record.Events) > 0 && tt.record.Events[0].ID == "MUTATED" {
					t.Error("modifying lv.Events mutated store record")
				}
			}
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
