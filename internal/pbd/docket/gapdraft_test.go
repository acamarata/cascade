package docket

import (
	"strings"
	"testing"
)

func TestMarkGapPendingWhenQGTicketNotLanded(t *testing.T) {
	gaps := []Gap{{ID: "g1", Origin: "o1", QG: "q1", Tokens: []string{"ACCEPT"}}}
	landed := map[string]bool{"q1": false}
	r, err := Mark(gaps, landed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Pending) != 1 || r.Pending[0] != "g1" {
		t.Errorf("expected pending g1, got %v", r.Pending)
	}
	if len(r.Drafts) != 0 {
		t.Errorf("expected no drafts, got %v", r.Drafts)
	}
}

func TestFoldRepeatedTokensIntoOriginGap(t *testing.T) {
	gaps := []Gap{{ID: "g1", Origin: "o1", QG: "q1", Tokens: []string{"ACCEPT", "ACCEPT", "DEFER"}}}
	landed := map[string]bool{"q1": true}
	r, err := Mark(gaps, landed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	folded := r.Folded["o1"]
	if len(folded) != 2 {
		t.Errorf("expected 2 folded tokens, got %v", folded)
	}
	if folded[0] != "ACCEPT" || folded[1] != "DEFER" {
		t.Errorf("unexpected folded tokens: %v", folded)
	}
}

func TestNoRedraftOnPendingGap(t *testing.T) {
	gaps := []Gap{{ID: "g1", Origin: "o1", QG: "q1", Tokens: []string{"ACCEPT"}}}
	landed := map[string]bool{"q1": false}
	r, err := Mark(gaps, landed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Drafts) != 0 {
		t.Errorf("expected no drafts for pending gap, got %v", r.Drafts)
	}
}

func TestRejectUnknownToken(t *testing.T) {
	gaps := []Gap{{ID: "g1", Origin: "o1", QG: "q1", Tokens: []string{"UNKNOWN"}}}
	landed := map[string]bool{"q1": true}
	_, err := Mark(gaps, landed)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "D5106") {
		t.Errorf("expected D5106 error, got %v", err)
	}
}
