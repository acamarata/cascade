package eligibility

import (
	"errors"
	"testing"
	"time"
)

// TestRegistryDefaultsDeny verifies that unregistered operations default to deny,
// expiry is enforced, and loosening changes are rejected.
func TestRegistryDefaultsDeny(t *testing.T) {
	r := NewMemoryRegistry()

	// Get returns deny by default for unregistered operations.
	row, err := r.Get("unknown-op")
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if row.PermittedBrowser {
		t.Errorf("expected PermittedBrowser to be false by default")
	}
	if row.ConsumerChatAutomation {
		t.Errorf("expected ConsumerChatAutomation to be false by default")
	}
	if row.ProviderAuthorised {
		t.Errorf("expected ProviderAuthorised to be false by default")
	}

	// Set a row.
	expiry := time.Now().Add(1 * time.Hour)
	baseRow := EligibilityRow{
		OperationID:            "op-1",
		TermsSource:            "system",
		LastReviewed:           time.Now(),
		Expiry:                 expiry,
		PermittedBrowser:       false,
		ConsumerChatAutomation: false,
		ProviderAuthorised:     false,
	}
	if err := r.Set(baseRow); err != nil {
		t.Fatalf("Set failed for base row: %v", err)
	}

	// Set rejects loosening changes without elevation.
	loosenedRow := baseRow
	loosenedRow.PermittedBrowser = true
	err = r.Set(loosenedRow)
	if !errors.Is(err, ErrLooseningWithoutElevation) {
		t.Errorf("expected ErrLooseningWithoutElevation, got: %v", err)
	}

	// Tightening is allowed.
	tightenedRow := loosenedRow
	tightenedRow.PermittedBrowser = false
	tightenedRow.ConsumerChatAutomation = false
	if err := r.Set(tightenedRow); err != nil {
		t.Errorf("Set failed for tightened row: %v", err)
	}

	// IsExpired returns true after Expiry time.
	expiredRow := baseRow
	expiredRow.Expiry = time.Now().Add(-1 * time.Hour)
	if !r.IsExpired(expiredRow, time.Now()) {
		t.Errorf("expected IsExpired to return true for expired row")
	}

	// IsExpired returns false before Expiry time.
	validRow := baseRow
	validRow.Expiry = time.Now().Add(1 * time.Hour)
	if r.IsExpired(validRow, time.Now()) {
		t.Errorf("expected IsExpired to return false for valid row")
	}

	// Consumer automation defaults to blocked without express authorisation.
	autoRow, _ := r.Get("auto-op")
	if autoRow.ConsumerChatAutomation {
		t.Errorf("expected ConsumerChatAutomation to be blocked by default")
	}
}
