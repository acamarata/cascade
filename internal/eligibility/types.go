// Package eligibility provides the surface-eligibility contract gate
// that adapters check before enabling mutations or browser routes.
package eligibility

import "time"

// EligibilityRow represents the eligibility state for a single operation.
// A row defaults to deny for consumer automation; permitted_browser and
// consumer_chat_automation default blocked unless provider_authorised is true.
type EligibilityRow struct {
	// OperationID uniquely identifies the operation.
	OperationID string
	// TermsSource records where the terms were sourced from.
	TermsSource string
	// LastReviewed records when the row was last reviewed.
	LastReviewed time.Time
	// Expiry records when the row expires.
	Expiry time.Time
	// PermittedBrowser indicates whether browser access is allowed.
	PermittedBrowser bool
	// ConsumerChatAutomation indicates whether consumer chat automation is allowed.
	ConsumerChatAutomation bool
	// ProviderAuthorised indicates whether the provider has expressly authorised the operation.
	ProviderAuthorised bool
}

// Registry defines the interface for managing eligibility rows.
type Registry interface {
	// Get returns the EligibilityRow for the given operationID.
	// It returns a default-deny row for unregistered operations.
	Get(operationID string) (EligibilityRow, error)
	// Set stores the given row. It rejects loosening changes without elevation;
	// only tightening is permitted at runtime.
	Set(row EligibilityRow) error
	// IsExpired returns true when now is after the row's Expiry time,
	// causing the row to read needs_review and the operation to be refused.
	IsExpired(row EligibilityRow, now time.Time) bool
}
