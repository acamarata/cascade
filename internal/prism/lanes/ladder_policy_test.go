package lanes

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNoAccountNameInCode ensures no hardcoded account names appear in the source.
func TestNoAccountNameInCode(t *testing.T) {
	src, err := readFileContent("ladder_policy.go")
	if err != nil {
		t.Fatalf("failed to read source: %v", err)
	}
	forbidden := []string{"acct-a", "acct-b", "acct-c", "acct-d"}
	for _, name := range forbidden {
		if strings.Contains(src, name) {
			t.Errorf("source contains hardcoded account name: %s", name)
		}
	}
}

// readFileContent is a helper to read file content for tests.
func readFileContent(name string) (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	dir := filepath.Dir(filename)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func TestReadPolicySuccess(t *testing.T) {
	cfg := Config{
		"lane_order":      []string{"secondary", "tertiary"},
		"ceilings":        map[string]int{"secondary": 100, "tertiary": 50},
		"window_duration": time.Hour,
	}
	policy, err := ReadPolicy(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(policy.LaneOrder) != 2 {
		t.Errorf("expected 2 lanes, got %d", len(policy.LaneOrder))
	}
	if policy.Ceilings["secondary"] != 100 {
		t.Errorf("expected ceiling 100, got %d", policy.Ceilings["secondary"])
	}
}

func TestReadPolicyMissingKeys(t *testing.T) {
	cfg := Config{}
	_, err := ReadPolicy(cfg)
	if err == nil {
		t.Fatal("expected error for missing keys")
	}
}

func TestSoonestExpiring(t *testing.T) {
	now := time.Now()
	lanes := []LaneState{
		{Ceiling: 10, Cap: false, Expiry: now.Add(2 * time.Hour), Parked: false, Idle: false},
		{Ceiling: 10, Cap: false, Expiry: now.Add(1 * time.Hour), Parked: false, Idle: false},
		{Ceiling: 10, Cap: true, Expiry: now.Add(30 * time.Minute), Parked: false, Idle: false},
		{Ceiling: 10, Cap: false, Expiry: now.Add(3 * time.Hour), Parked: true, Idle: false},
	}
	idx := SoonestExpiring(lanes)
	if idx != 1 {
		t.Errorf("expected index 1, got %d", idx)
	}
}

func TestSoonestExpiringNoneQualify(t *testing.T) {
	now := time.Now()
	lanes := []LaneState{
		{Ceiling: 10, Cap: true, Expiry: now.Add(1 * time.Hour), Parked: false, Idle: false},
		{Ceiling: 10, Cap: false, Expiry: now.Add(2 * time.Hour), Parked: true, Idle: false},
	}
	idx := SoonestExpiring(lanes)
	if idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}
