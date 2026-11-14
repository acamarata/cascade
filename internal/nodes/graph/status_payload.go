package graph

// NodeState represents the current state and readiness of a node.
type NodeState struct {
	// Healthy indicates whether the node is functioning correctly.
	Healthy bool
	// OpsToolVerdict is the readiness verdict from opstool.
	OpsToolVerdict string
	// TdarrVerdict is the readiness verdict from Tdarr.
	TdarrVerdict string
}

// StatusPayload represents the status information of a node.
type StatusPayload struct {
	// OpsToolVerdict is the opstool readiness verdict included in the payload.
	OpsToolVerdict string
	// TdarrVerdict is the Tdarr readiness verdict included in the payload.
	TdarrVerdict string
}

// BuildStatus creates a StatusPayload from the given NodeState.
// When the node is unhealthy, it includes the opstool and Tdarr verdicts.
// If an unhealthy node is missing a verdict, it returns "unknown" for that field.
// When the node is healthy, the verdict fields are omitted (empty strings).
func BuildStatus(node NodeState) StatusPayload {
	if node.Healthy {
		return StatusPayload{}
	}

	opstool := node.OpsToolVerdict
	if opstool == "" {
		opstool = "unknown"
	}

	tdarr := node.TdarrVerdict
	if tdarr == "" {
		tdarr = "unknown"
	}

	return StatusPayload{
		OpsToolVerdict: opstool,
		TdarrVerdict:   tdarr,
	}
}
