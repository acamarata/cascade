package mod

import (
	"strings"
	"testing"
)

func TestDistinctPrefixesPass(t *testing.T) {
	err := ValidatePrefixes([]string{"st-a", "st-b"}, []string{"rl-a", "rl-b"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSharedPrefixFails(t *testing.T) {
	err := ValidatePrefixes([]string{"st-a"}, []string{"st-b"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "st") {
		t.Fatalf("expected error to mention 'st', got %v", err)
	}
}

func TestMixedPrefixWithinGroupFails(t *testing.T) {
	err := ValidatePrefixes([]string{"st-a", "xx-b"}, []string{"rl-a"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestEmptyGroupFailsClosed(t *testing.T) {
	err := ValidatePrefixes([]string{"st-a"}, []string{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
