package background

import (
	"context"
	"errors"
	"testing"
)

// mockSubChats is a test double for SubChats that records calls.
type mockSubChats struct {
	openCalls int
	waitCalls int
}

func (m *mockSubChats) Open(_ context.Context, req SubChatRequest) (string, error) {
	m.openCalls++
	return "id", nil
}

func (m *mockSubChats) Wait(_ context.Context, id string) (string, error) {
	m.waitCalls++
	return "result", nil
}

// TestSubSpawnMissingRoleFailsClosed verifies that an empty or whitespace Role returns ErrRoleRequired,
// does not invoke Open/Wait, and leaves the registry unchanged.
func TestSubSpawnMissingRoleFailsClosed(t *testing.T) {
	ctx := context.Background()
	chats := &mockSubChats{}
	registry := SubSpawnRegistry{
		"task1": func(_ context.Context, req SubSpawnRequest) (SubSpawnReport, error) {
			return SubSpawnReport{ID: "should-not-run"}, nil
		},
	}
	originalLen := len(registry)

	cases := []struct {
		name string
		role string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"tab", "\t"},
		{"newline", "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := SubSpawnRequest{
				Task:  "task1",
				Role:  tc.role,
				Scope: "scope1",
			}
			report, err := SubSpawn(ctx, chats, registry, req)
			if !errors.Is(err, ErrRoleRequired) {
				t.Fatalf("expected ErrRoleRequired, got %v", err)
			}
			if err.Error() != "role is required" {
				t.Fatalf("expected error text 'role is required', got %q", err.Error())
			}
			if report.ID != "" || report.Handle != "" || report.Status != "" || report.Report != "" || report.Metadata != nil {
				t.Fatalf("expected zero report, got %+v", report)
			}
			if chats.openCalls != 0 {
				t.Fatalf("expected 0 Open calls, got %d", chats.openCalls)
			}
			if chats.waitCalls != 0 {
				t.Fatalf("expected 0 Wait calls, got %d", chats.waitCalls)
			}
			if len(registry) != originalLen {
				t.Fatalf("registry length changed: expected %d, got %d", originalLen, len(registry))
			}
		})
	}
}
