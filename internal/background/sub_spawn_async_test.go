package background

import (
	"context"
	"errors"
	"testing"
)

type fakeSubChats struct {
	openCalled int
	lastReq    SubChatRequest
	waitCalled int
	id         string
	err        error
}

func (f *fakeSubChats) Open(ctx context.Context, req SubChatRequest) (string, error) {
	f.openCalled++
	f.lastReq = req
	if f.err != nil {
		return "", f.err
	}
	return f.id, nil
}

func (f *fakeSubChats) Wait(ctx context.Context, id string) (string, error) {
	f.waitCalled++
	return "report", nil
}

// TestSubSpawnScopedSubChat verifies Open is called with exact fields and metadata.
func TestSubSpawnScopedSubChat(t *testing.T) {
	expectedMeta := map[string]string{
		"tool":       "sub_spawn",
		"P2-PEWB-17": "preserved",
		"P2-ADVX-34": "preserved",
		"P2-HRNC-39": "preserved",
		"P2-LIFE-25": "preserved",
		"D4742":      "preserved",
		"D6044":      "preserved",
		"D6048":      "preserved",
		"D6099":      "preserved",
	}

	t.Run("success", func(t *testing.T) {
		fake := &fakeSubChats{id: "subchat:1"}
		ctx := context.Background()
		req := SubSpawnRequest{Task: "summarize", Role: "worker", Scope: "repo"}

		report, err := SubSpawn(ctx, fake, nil, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.openCalled != 1 {
			t.Fatalf("expected Open called once, got %d", fake.openCalled)
		}
		if fake.lastReq.Task != "summarize" {
			t.Errorf("Task = %q, want %q", fake.lastReq.Task, "summarize")
		}
		if fake.lastReq.Role != "worker" {
			t.Errorf("Role = %q, want %q", fake.lastReq.Role, "worker")
		}
		if fake.lastReq.Scope != "repo" {
			t.Errorf("Scope = %q, want %q", fake.lastReq.Scope, "repo")
		}
		if len(fake.lastReq.Metadata) != len(expectedMeta) {
			t.Fatalf("metadata length = %d, want %d", len(fake.lastReq.Metadata), len(expectedMeta))
		}
		for k, v := range expectedMeta {
			if fake.lastReq.Metadata[k] != v {
				t.Errorf("metadata[%q] = %q, want %q", k, fake.lastReq.Metadata[k], v)
			}
		}
		if report.ID != "subchat:1" {
			t.Errorf("ID = %q, want %q", report.ID, "subchat:1")
		}
	})

	t.Run("registry replaces agent_spawn", func(t *testing.T) {
		fake := &fakeSubChats{id: "subchat:1"}
		reg := SubSpawnRegistry{
			"agent_spawn": func(ctx context.Context, r SubSpawnRequest) (SubSpawnReport, error) {
				return SubSpawnReport{}, errors.New("old")
			},
			"other_tool": func(ctx context.Context, r SubSpawnRequest) (SubSpawnReport, error) {
				return SubSpawnReport{ID: "other"}, nil
			},
		}
		ctx := context.Background()
		req := SubSpawnRequest{Task: "summarize", Role: "worker", Scope: "repo"}

		_, err := SubSpawn(ctx, fake, reg, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := reg["sub_spawn"]; !ok {
			t.Error("expected sub_spawn in registry")
		}
		if _, ok := reg["other_tool"]; !ok {
			t.Error("expected other_tool retained in registry")
		}
	})

	t.Run("blank task rejected", func(t *testing.T) {
		fake := &fakeSubChats{id: "subchat:1"}
		reg := SubSpawnRegistry{"sub_spawn": func(ctx context.Context, r SubSpawnRequest) (SubSpawnReport, error) { return SubSpawnReport{}, nil }}
		_, err := SubSpawn(context.Background(), fake, reg, SubSpawnRequest{Task: "", Role: "w", Scope: "s"})
		if !errors.Is(err, ErrInvalidSubSpawn) {
			t.Errorf("err = %v, want ErrInvalidSubSpawn", err)
		}
	})

	t.Run("blank scope rejected", func(t *testing.T) {
		fake := &fakeSubChats{id: "subchat:1"}
		reg := SubSpawnRegistry{"sub_spawn": func(ctx context.Context, r SubSpawnRequest) (SubSpawnReport, error) { return SubSpawnReport{}, nil }}
		_, err := SubSpawn(context.Background(), fake, reg, SubSpawnRequest{Task: "t", Role: "w", Scope: ""})
		if !errors.Is(err, ErrInvalidSubSpawn) {
			t.Errorf("err = %v, want ErrInvalidSubSpawn", err)
		}
	})

	t.Run("malformed ID rejected", func(t *testing.T) {
		fake := &fakeSubChats{id: "bad-id"}
		_, err := SubSpawn(context.Background(), fake, nil, SubSpawnRequest{Task: "t", Role: "w", Scope: "s"})
		if !errors.Is(err, ErrSubChat) {
			t.Errorf("err = %v, want ErrSubChat", err)
		}
	})

	t.Run("nil dependencies rejected", func(t *testing.T) {
		_, err := SubSpawn(context.Background(), nil, nil, SubSpawnRequest{Task: "t", Role: "w", Scope: "s"})
		if !errors.Is(err, ErrInvalidSubSpawn) {
			t.Errorf("err = %v, want ErrInvalidSubSpawn", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		fake := &fakeSubChats{id: "subchat:1"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := SubSpawn(ctx, fake, nil, SubSpawnRequest{Task: "t", Role: "w", Scope: "s"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// TestSubSpawnRoleParameter verifies Role is copied exactly into SubChatRequest.
func TestSubSpawnRoleParameter(t *testing.T) {
	fake := &fakeSubChats{id: "subchat:1"}
	ctx := context.Background()
	req := SubSpawnRequest{Task: "summarize", Role: "reviewer", Scope: "repo"}

	_, err := SubSpawn(ctx, fake, nil, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastReq.Role != "reviewer" {
		t.Errorf("Role = %q, want %q", fake.lastReq.Role, "reviewer")
	}
	keys := []string{"P2-PEWB-17", "P2-ADVX-34", "P2-HRNC-39", "P2-LIFE-25", "D4742", "D6044", "D6048", "D6099"}
	for _, k := range keys {
		if fake.lastReq.Metadata[k] != "preserved" {
			t.Errorf("metadata[%q] = %q, want preserved", k, fake.lastReq.Metadata[k])
		}
	}
	if fake.lastReq.Metadata["tool"] != "sub_spawn" {
		t.Errorf("metadata[tool] = %q, want sub_spawn", fake.lastReq.Metadata["tool"])
	}
	if len(fake.lastReq.Metadata) != 9 {
		t.Errorf("metadata length = %d, want 9", len(fake.lastReq.Metadata))
	}
}

// TestSubSpawnAsyncReturnsHandle verifies async path returns pending handle without calling Wait.
func TestSubSpawnAsyncReturnsHandle(t *testing.T) {
	fake := &fakeSubChats{id: "subchat:1"}
	ctx := context.Background()
	req := SubSpawnRequest{Task: "summarize", Role: "worker", Scope: "repo", Async: true}

	report, err := SubSpawn(ctx, fake, nil, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Status != "pending" {
		t.Errorf("Status = %q, want %q", report.Status, "pending")
	}
	if report.ID != "subchat:1" {
		t.Errorf("ID = %q, want %q", report.ID, "subchat:1")
	}
	if report.Handle != "subchat:1" {
		t.Errorf("Handle = %q, want %q", report.Handle, "subchat:1")
	}
	if report.Report != "" {
		t.Errorf("Report = %q, want empty", report.Report)
	}
	if fake.waitCalled != 0 {
		t.Errorf("Wait called %d times, want 0", fake.waitCalled)
	}
}
