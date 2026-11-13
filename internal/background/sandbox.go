package background

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrInvalidSpawn is returned when the spawn event or lint closure is invalid.
var ErrInvalidSpawn = errors.New("invalid spawn")

// ErrSpawnLint is returned when the advisory lint callback fails.
var ErrSpawnLint = errors.New("spawn lint failed")

// SpawnEvent holds the parameters for spawning an agent.
type SpawnEvent struct {
	Agent  string
	Prompt string
}

// AdvisoryLint is a closure that returns advisory violations.
type AdvisoryLint func() ([]string, error)

type lintResult struct {
	Advisory   bool     `json:"advisory"`
	Violations []string `json:"violations"`
}

// AgentLogHook produces compact JSON lint output and spawn metadata.
// It never inspects or passes e.Prompt, and suppresses callback errors.
func AgentLogHook(e SpawnEvent, lint AdvisoryLint) (lintJSON, metadata string, err error) {
	if strings.TrimSpace(e.Agent) == "" || lint == nil {
		return "", "", ErrInvalidSpawn
	}

	violations, lerr := lint()
	if lerr != nil {
		return "", "", ErrSpawnLint
	}

	// Only return metadata when violations is explicitly an empty slice, not nil.
	if violations != nil && len(violations) == 0 {
		type meta struct {
			Kind  string `json:"kind"`
			Agent string `json:"agent"`
		}
		mb, merr := json.Marshal(meta{Kind: "spawn", Agent: e.Agent})
		if merr != nil {
			return "", "", ErrSpawnLint
		}
		metadata = string(mb) + "\n"
	}

	// For lintJSON, nil encodes as an empty array.
	if violations == nil {
		violations = []string{}
	}

	res := lintResult{
		Advisory:   true,
		Violations: violations,
	}

	b, jerr := json.Marshal(res)
	if jerr != nil {
		return "", "", ErrSpawnLint
	}

	return string(b) + "\n", metadata, nil
}
