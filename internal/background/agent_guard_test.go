package background

import (
	"testing"
)

const fixtureRunToken = "sc-run-token"

func TestWebchatQueryAllowedBeforeSearch(t *testing.T) {
	err := CheckWebchatQuery(false)
	if err != nil {
		t.Fatalf("expected nil error before search, got: %v", err)
	}
}

func TestWebchatQueryDeniedAfterLocalSearch(t *testing.T) {
	err := CheckWebchatQuery(true)
	if err == nil {
		t.Fatal("expected error after local search, got nil")
	}
	guardErr, ok := err.(GuardError)
	if !ok {
		t.Fatalf("expected GuardError, got %T", err)
	}
	if guardErr.Code != "D3206" {
		t.Errorf("expected code D3206, got %s", guardErr.Code)
	}
}

func TestAgentListAllowedWithRunToken(t *testing.T) {
	err := CheckAgentOp(OpList, fixtureRunToken)
	if err != nil {
		t.Fatalf("expected nil error for list with run token, got: %v", err)
	}
}

func TestAgentShowAllowedWithRunToken(t *testing.T) {
	err := CheckAgentOp(OpShow, fixtureRunToken)
	if err != nil {
		t.Fatalf("expected nil error for show with run token, got: %v", err)
	}
}

func TestAgentSpawnDeniedWithoutRunToken(t *testing.T) {
	err := CheckAgentOp(OpSpawn, "")
	if err == nil {
		t.Fatal("expected error for spawn without run token, got nil")
	}
	guardErr, ok := err.(GuardError)
	if !ok {
		t.Fatalf("expected GuardError, got %T", err)
	}
	if guardErr.Code != "D3318" {
		t.Errorf("expected code D3318, got %s", guardErr.Code)
	}
}
