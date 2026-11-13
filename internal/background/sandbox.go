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

// AgentLogHook produces compact JSON lint output and empty metadata.
func AgentLogHook(e SpawnEvent, lint AdvisoryLint) (lintJSON, metadata string, err error) {
	if strings.TrimSpace(e.Agent) == "" || lint == nil {
		return "", "", ErrInvalidSpawn
	}

	violations, lerr := lint()
	if lerr != nil {
		return "", "", ErrSpawnLint
	}

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

	return string(b) + "\n", "", nil
}
