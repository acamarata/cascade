// Package farm provides local model lifecycle utilities.
package farm

import (
	"testing"
	"time"
)

// TestKeepWarmTypeExists verifies that the KeepWarm type and ShouldUnload
// function exist with the expected signatures.
func TestKeepWarmTypeExists(t *testing.T) {
	policy := KeepWarm{IdleMinutes: 10}
	if policy.IdleMinutes != 10 {
		t.Fatalf("expected IdleMinutes 10, got %d", policy.IdleMinutes)
	}

	lastUse := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := lastUse.Add(5 * time.Minute)

	// Should not unload when within idle window.
	if ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected ShouldUnload to return false within idle window")
	}

	// Should unload when past idle window.
	now = lastUse.Add(11 * time.Minute)
	if !ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected ShouldUnload to return true past idle window")
	}
}

// TestIdleModelUnloads verifies that a model is unloaded when the elapsed
// time since last use strictly exceeds the IdleMinutes window.
func TestIdleModelUnloads(t *testing.T) {
	policy := KeepWarm{IdleMinutes: 10}
	lastUse := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	now := lastUse.Add(10 * time.Minute)
	if ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected false exactly at idle boundary")
	}

	now = lastUse.Add(10*time.Minute + time.Second)
	if !ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected true past idle boundary")
	}
}

// TestKeepWarmDefaultTenMinutes verifies that the default policy of 10
// minutes satisfies the retention requirement.
func TestKeepWarmDefaultTenMinutes(t *testing.T) {
	defaultPolicy := KeepWarm{IdleMinutes: 10}
	if defaultPolicy.IdleMinutes != 10 {
		t.Fatalf("expected default IdleMinutes 10, got %d", defaultPolicy.IdleMinutes)
	}

	lastUse := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	now := lastUse.Add(9 * time.Minute)
	if ShouldUnload(lastUse, now, defaultPolicy) {
		t.Fatal("expected retention within 10 minutes")
	}

	now = lastUse.Add(11 * time.Minute)
	if !ShouldUnload(lastUse, now, defaultPolicy) {
		t.Fatal("expected unload after 10 minutes")
	}
}

// TestLocalModelOnlyWhenNeeded verifies that WantsLocal returns true only for
// decision model calls, privacy-bound chats, and offline runs.
func TestLocalModelOnlyWhenNeeded(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want bool
	}{
		{"decision model", Task{IsDecision: true}, true},
		{"private chat", Task{IsPrivateChat: true}, true},
		{"offline run", Task{IsOffline: true}, true},
		{"decision and offline", Task{IsDecision: true, IsOffline: true}, true},
		{"hosted task", Task{}, false},
		{"gateway task", Task{IsDecision: false, IsPrivateChat: false, IsOffline: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsLocal(tc.task); got != tc.want {
				t.Errorf("WantsLocal(%+v) = %v, want %v", tc.task, got, tc.want)
			}
		})
	}
}

// TestDefaultIsHostedOrGateway verifies that a task with no special flags
// is routed to hosted or gateway (WantsLocal returns false).
func TestDefaultIsHostedOrGateway(t *testing.T) {
	task := Task{}
	if WantsLocal(task) {
		t.Fatal("expected WantsLocal to return false for default task (hosted/gateway)")
	}
}

// TestPrivateChatStaysLocal verifies that a privacy-bound chat session
// always executes locally.
func TestPrivateChatStaysLocal(t *testing.T) {
	task := Task{IsPrivateChat: true}
	if !WantsLocal(task) {
		t.Fatal("expected WantsLocal to return true for private chat")
	}
}
