package hj

import (
	"sync"
	"testing"
)

func TestPersistTakenState(t *testing.T) {
	r := Receipt{ID: "test-persist-taken-1", State: Taken}
	got, err := Persist(r)
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got != r {
		t.Errorf("Persist() = %v, want %v", got, r)
	}
	found := false
	for _, rec := range Receipts() {
		if rec.ID == r.ID && rec.State == r.State {
			found = true
		}
	}
	if !found {
		t.Errorf("Receipts() did not contain persisted receipt %v", r)
	}
}

func TestPersistAttemptState(t *testing.T) {
	r := Receipt{ID: "test-persist-attempt-1", State: ReceiptAttempt}
	got, err := Persist(r)
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got != r {
		t.Errorf("Persist() = %v, want %v", got, r)
	}
	if got.State != "Attempt" {
		t.Errorf("got.State = %v, want Attempt", got.State)
	}
}

func TestPersistFailState(t *testing.T) {
	r := Receipt{ID: "test-persist-fail-1", State: Fail}
	got, err := Persist(r)
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got != r {
		t.Errorf("Persist() = %v, want %v", got, r)
	}
	found := false
	for _, rec := range Receipts() {
		if rec.ID == r.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Receipts() did not contain persisted fail receipt")
	}
}

func TestPersistTurnEndedState(t *testing.T) {
	r := Receipt{ID: "test-persist-turnended-1", State: TurnEnded}
	got, err := Persist(r)
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got != r {
		t.Errorf("Persist() = %v, want %v", got, r)
	}
	found := false
	for _, rec := range Receipts() {
		if rec.ID == r.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Receipts() did not contain persisted turnended receipt")
	}
}

func TestPersistOtherStates(t *testing.T) {
	states := []ReceiptState{End, Ready, Due, For, CountsFor, Prune}
	expectedIDs := make(map[string]bool)
	for _, st := range states {
		expectedIDs["test-persist-other-"+string(st)] = true
	}
	var wg sync.WaitGroup
	for _, st := range states {
		wg.Add(1)
		go func(st ReceiptState) {
			defer wg.Done()
			id := "test-persist-other-" + string(st)
			r := Receipt{ID: id, State: st}
			got, err := Persist(r)
			if err != nil {
				t.Errorf("Persist(%v) error = %v", r, err)
				return
			}
			if got != r {
				t.Errorf("Persist(%v) = %v, want %v", r, got, r)
			}
		}(st)
	}
	wg.Wait()

	all := Receipts()
	if len(all) == 0 {
		t.Fatal("Receipts() returned empty")
	}
	// Check sorted
	for i := 1; i < len(all); i++ {
		if all[i-1].ID > all[i].ID {
			t.Errorf("Receipts() not sorted: %v > %v", all[i-1].ID, all[i].ID)
		}
	}
	// Check presence
	for id := range expectedIDs {
		found := false
		for _, r := range all {
			if r.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Receipts() missing %s", id)
		}
	}

	// Check modification of snapshot doesn't affect store
	for i := range all {
		all[i].ID = "modified-" + all[i].ID
	}
	all2 := Receipts()
	for _, r := range all2 {
		if r.ID == "modified-test-persist-other-End" {
			t.Error("modifying snapshot modified the store")
		}
	}
}
