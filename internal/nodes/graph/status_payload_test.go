package graph

import "testing"

// TestUnhealthyStatusHasBothVerdicts verifies that an unhealthy node with
// opstool "ready" and tdarr "not_ready" yields both verdict fields.
func TestUnhealthyStatusHasBothVerdicts(t *testing.T) {
	node := NodeState{
		Healthy:        false,
		OpsToolVerdict: "ready",
		TdarrVerdict:   "not_ready",
	}
	payload := BuildStatus(node)
	if payload.OpsToolVerdict != "ready" {
		t.Errorf("expected opstool_verdict %q, got %q", "ready", payload.OpsToolVerdict)
	}
	if payload.TdarrVerdict != "not_ready" {
		t.Errorf("expected tdarr_verdict %q, got %q", "not_ready", payload.TdarrVerdict)
	}
}

// TestHealthyStatusOmitsVerdicts verifies that a healthy node yields empty
// verdict fields.
func TestHealthyStatusOmitsVerdicts(t *testing.T) {
	node := NodeState{
		Healthy:        true,
		OpsToolVerdict: "ready",
		TdarrVerdict:   "ready",
	}
	payload := BuildStatus(node)
	if payload.OpsToolVerdict != "" {
		t.Errorf("expected empty opstool_verdict for healthy node, got %q", payload.OpsToolVerdict)
	}
	if payload.TdarrVerdict != "" {
		t.Errorf("expected empty tdarr_verdict for healthy node, got %q", payload.TdarrVerdict)
	}
}

// TestUnhealthyMissingVerdictIsUnknown verifies that an unhealthy node with
// no tdarr verdict yields tdarr_verdict "unknown".
func TestUnhealthyMissingVerdictIsUnknown(t *testing.T) {
	node := NodeState{
		Healthy:        false,
		OpsToolVerdict: "ready",
	}
	payload := BuildStatus(node)
	if payload.TdarrVerdict != "unknown" {
		t.Errorf("expected tdarr_verdict %q, got %q", "unknown", payload.TdarrVerdict)
	}
}
