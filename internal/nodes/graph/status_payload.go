package graph

// NodeState represents the current state and readiness of a node.
type NodeState struct {
	// Healthy indicates whether the node is functioning correctly.
	Healthy bool
	// CamVerdict is the readiness verdict from cam.
	CamVerdict string
	// TdarrVerdict is the readiness verdict from Tdarr.
	TdarrVerdict string
}

// StatusPayload represents the status information of a node.
type StatusPayload struct {
	// CamVerdict is the cam readiness verdict included in the payload.
	CamVerdict string
	// TdarrVerdict is the Tdarr readiness verdict included in the payload.
	TdarrVerdict string
}

// BuildStatus creates a StatusPayload from the given NodeState.
// When the node is unhealthy, it includes the cam and Tdarr verdicts.
// If an unhealthy node is missing a verdict, it returns "unknown" for that field.
// When the node is healthy, the verdict fields are omitted (empty strings).
func BuildStatus(node NodeState) StatusPayload {
	if node.Healthy {
		return StatusPayload{}
	}

	cam := node.CamVerdict
	if cam == "" {
		cam = "unknown"
	}

	tdarr := node.TdarrVerdict
	if tdarr == "" {
		tdarr = "unknown"
	}

	return StatusPayload{
		CamVerdict:   cam,
		TdarrVerdict: tdarr,
	}
}
