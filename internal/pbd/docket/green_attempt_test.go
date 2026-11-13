package docket

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestGreenAttemptStoresExactSHA(t *testing.T) {
	sha := "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	cost := 1.0
	ga, err := NewGreenAttempt(sha, &cost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ga.CommitSHA != sha {
		t.Errorf("expected SHA %q, got %q", sha, ga.CommitSHA)
	}
}

func TestGreenAttemptCostNilMarshalsNull(t *testing.T) {
	ga, err := NewGreenAttempt("abc", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(ga)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if val, ok := m["cost"]; !ok || val != nil {
		t.Errorf("expected cost to be JSON null, got %v", val)
	}
}

func TestGreenAttemptCostNumericMarshals(t *testing.T) {
	cost := 0.42
	ga, err := NewGreenAttempt("abc", &cost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(ga)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	val, ok := m["cost"]
	if !ok {
		t.Fatalf("cost missing in marshaled JSON")
	}
	f, ok := val.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T", val)
	}
	if f != 0.42 {
		t.Errorf("expected cost 0.42, got %v", f)
	}
}

func TestGreenAttemptEmptySHARejected(t *testing.T) {
	_, err := NewGreenAttempt("", nil)
	if !errors.Is(err, ErrGreenSHAEmpty) {
		t.Errorf("expected ErrGreenSHAEmpty, got %v", err)
	}
}
