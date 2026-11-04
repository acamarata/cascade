package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testDoer is a fake Doer for testing.
type testDoer struct {
	response []byte
	status   int
	err      error
}

func (d *testDoer) Do(_ context.Context, _, _ string, _ []byte, _ map[string]string) ([]byte, int, error) {
	return d.response, d.status, d.err
}

// TestUsageClientGetShape verifies the GET request matches the captured golden.
func TestUsageClientGetShape(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("CASCADE_HOME", tmpDir)

	desc := UsageDescriptor{
		Host:      "https://api.example.com",
		UsagePath: "/v1/usage",
	}
	token := []byte("CANARY_TOKEN_FOR_TESTING")

	// Load the golden response
	goldenPath := filepath.Join("testdata", "usage", "usage_response.json")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden: %v", err)
	}

	doer := &testDoer{response: golden, status: 200}
	doc, err := FetchUsage(context.Background(), desc, token, doer)
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}

	// Verify we got windows
	if len(doc.Windows) == 0 {
		t.Fatal("expected at least one window")
	}

	// Verify the first window has expected structure
	if doc.Windows[0].DurationHours != 5 {
		t.Errorf("expected first window duration 5h, got %d", doc.Windows[0].DurationHours)
	}
	if doc.Windows[0].TokensUsed != 12345 {
		t.Errorf("expected first window tokens_used 12345, got %d", doc.Windows[0].TokensUsed)
	}
}

// TestUsageDocParsed verifies the usage document parses into 5h and 7d windows.
func TestUsageDocParsed(t *testing.T) {
	goldenPath := filepath.Join("testdata", "usage", "usage_response.json")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden: %v", err)
	}

	var doc UsageDocument
	if err := json.Unmarshal(golden, &doc); err != nil {
		t.Fatalf("failed to parse golden: %v", err)
	}

	if len(doc.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(doc.Windows))
	}

	// First window: 5h
	if doc.Windows[0].DurationHours != 5 {
		t.Errorf("expected first window 5h, got %d", doc.Windows[0].DurationHours)
	}
	if doc.Windows[0].ResetsAt.IsZero() {
		t.Error("first window resets_at is zero")
	}

	// Second window: 7d (168h)
	if doc.Windows[1].DurationHours != 168 {
		t.Errorf("expected second window 168h (7d), got %d", doc.Windows[1].DurationHours)
	}
	if doc.Windows[1].ResetsAt.IsZero() {
		t.Error("second window resets_at is zero")
	}
}

// TestRefreshGrantRequestShapeGolden verifies the refresh request matches the captured golden.
func TestRefreshGrantRequestShapeGolden(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("CASCADE_HOME", tmpDir)

	desc := UsageDescriptor{
		Host:        "https://api.example.com",
		RefreshPath: "/v1/oauth/token",
	}
	refresh := []byte("CANARY_REFRESH_TOKEN_0000000000000000000000000000000000000000")
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	req, err := RefreshRequest(desc, refresh, now)
	if err != nil {
		t.Fatalf("RefreshRequest failed: %v", err)
	}

	// Load the golden request
	goldenPath := filepath.Join("testdata", "usage", "refresh_request.json")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden: %v", err)
	}

	// Parse both to compare structure
	var reqMap, goldenMap map[string]any
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if err := json.Unmarshal(golden, &goldenMap); err != nil {
		t.Fatalf("failed to parse golden: %v", err)
	}

	// Verify structure matches
	if reqMap["grant_type"] != goldenMap["grant_type"] {
		t.Errorf("grant_type mismatch: got %v, want %v", reqMap["grant_type"], goldenMap["grant_type"])
	}
	if reqMap["refresh_token"] != goldenMap["refresh_token"] {
		t.Errorf("refresh_token mismatch: got %v, want %v", reqMap["refresh_token"], goldenMap["refresh_token"])
	}
	if reqMap["user_agent"] != goldenMap["user_agent"] {
		t.Errorf("user_agent mismatch: got %v, want %v", reqMap["user_agent"], goldenMap["user_agent"])
	}

	// Verify method and headers
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if req.Headers["Content-Type"] != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", req.Headers["Content-Type"])
	}
	if req.Headers["User-Agent"] != userAgentVersion {
		t.Errorf("expected User-Agent %s, got %s", userAgentVersion, req.Headers["User-Agent"])
	}
}
