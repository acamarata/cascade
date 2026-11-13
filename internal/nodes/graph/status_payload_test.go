package graph

import "testing"

// TestUnhealthyStatusHasBothVerdicts verifies that an unhealthy node with
// cam "ready" and tdarr "not_ready" yields both verdict fields.
func TestUnhealthyStatusHasBothVerdicts(t *testing.T) {
	node := NodeState{
		Healthy:      false,
		CamVerdict:   "ready",
		TdarrVerdict: "not_ready",
	}
	payload := BuildStatus(node)
	if payload.CamVerdict != "ready" {
		t.Errorf("expected cam_verdict %q, got %q", "ready", payload.CamVerdict)
	}
	if payload.TdarrVerdict != "not_ready" {
		t.Errorf("expected tdarr_verdict %q, got %q", "not_ready", payload.TdarrVerdict)
	}
}

// TestHealthyStatusOmitsVerdicts verifies that a healthy node yields empty
// verdict fields.
func TestHealthyStatusOmitsVerdicts(t *testing.T) {
	node := NodeState{
		Healthy:      true,
		CamVerdict:   "ready",
		TdarrVerdict: "ready",
	}
	payload := BuildStatus(node)
	if payload.CamVerdict != "" {
		t.Errorf("expected empty cam_verdict for healthy node, got %q", payload.CamVerdict)
	}
	if payload.TdarrVerdict != "" {
		t.Errorf("expected empty tdarr_verdict for healthy node, got %q", payload.TdarrVerdict)
	}
}

// TestUnhealthyMissingVerdictIsUnknown verifies that an unhealthy node with
// no tdarr verdict yields tdarr_verdict "unknown".
func TestUnhealthyMissingVerdictIsUnknown(t *testing.T) {
	node := NodeState{
		Healthy:    false,
		CamVerdict: "ready",
	}
	payload := BuildStatus(node)
	if payload.TdarrVerdict != "unknown" {
		t.Errorf("expected tdarr_verdict %q, got %q", "unknown", payload.TdarrVerdict)
	}
}
