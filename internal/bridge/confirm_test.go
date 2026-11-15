package bridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestConfirmTypesCompile verifies that the ConfirmToken struct,
// sentinel errors, and ConfirmStore interface are correctly defined.
func TestConfirmTypesCompile(t *testing.T) {
	now := time.Now()
	tok := ConfirmToken{
		Token:     "test-token",
		Action:    "gpt.purge",
		Subjects:  []string{"a", "b"},
		ExpiresAt: now,
	}

	if tok.Token != "test-token" {
		t.Fatalf("expected Token %q, got %q", "test-token", tok.Token)
	}
	if tok.Action != "gpt.purge" {
		t.Fatalf("expected Action %q, got %q", "gpt.purge", tok.Action)
	}
	if len(tok.Subjects) != 2 {
		t.Fatalf("expected 2 Subjects, got %d", len(tok.Subjects))
	}
	if !tok.ExpiresAt.Equal(now) {
		t.Fatalf("expected ExpiresAt %v, got %v", now, tok.ExpiresAt)
	}

	sentinels := []error{
		ErrTokenNotFound,
		ErrTokenExpired,
		ErrSubjectsMismatch,
		ErrTokenAlreadyUsed,
	}
	for _, err := range sentinels {
		if err == nil {
			t.Fatal("sentinel error must not be nil")
		}
	}

	if !errors.Is(ErrTokenNotFound, ErrTokenNotFound) {
		t.Fatal("ErrTokenNotFound identity check failed")
	}
	if !errors.Is(ErrTokenExpired, ErrTokenExpired) {
		t.Fatal("ErrTokenExpired identity check failed")
	}
	if !errors.Is(ErrSubjectsMismatch, ErrSubjectsMismatch) {
		t.Fatal("ErrSubjectsMismatch identity check failed")
	}
	if !errors.Is(ErrTokenAlreadyUsed, ErrTokenAlreadyUsed) {
		t.Fatal("ErrTokenAlreadyUsed identity check failed")
	}

	var _ ConfirmStore = (*mockConfirmStore)(nil)
}

type mockConfirmStore struct{}

func (m *mockConfirmStore) Issue(ctx context.Context, action string, subjects []string) (ConfirmToken, error) {
	return ConfirmToken{}, nil
}

func (m *mockConfirmStore) Redeem(ctx context.Context, token string, action string, subjects []string) error {
	return nil
}
