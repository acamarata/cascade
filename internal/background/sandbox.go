package background

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// ErrShellDenied is returned when a shell command is requested.
var ErrShellDenied = errors.New("D3127")

// ErrOutsideRead is returned when a path escapes the authorized root.
var ErrOutsideRead = errors.New("D4331")

// PrintRequest holds the parameters for CheckPrint authorization.
type PrintRequest struct {
	Folder  string
	Path    string
	Command string
}

// CheckPrint authorizes a print request without executing or reading content.
// It returns ErrShellDenied for any command and ErrOutsideRead for escaping paths.
func CheckPrint(req PrintRequest) (string, error) {
	if req.Command != "" {
		return "", ErrShellDenied
	}
	if req.Folder == "" || req.Path == "" {
		return "", ErrOutsideRead
	}

	folder, err := filepath.Abs(req.Folder)
	if err != nil {
		return "", ErrOutsideRead
	}
	folder, err = filepath.EvalSymlinks(folder)
	if err != nil {
		return "", ErrOutsideRead
	}

	fi, err := os.Stat(folder)
	if err != nil || !fi.IsDir() {
		return "", ErrOutsideRead
	}

	var target string
	if filepath.IsAbs(req.Path) {
		target = req.Path
	} else {
		target = filepath.Join(folder, req.Path)
	}

	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", ErrOutsideRead
	}

	rel, err := filepath.Rel(folder, target)
	if err != nil {
		return "", ErrOutsideRead
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRead
	}

	return "", nil
}
