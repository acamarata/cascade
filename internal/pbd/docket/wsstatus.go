package docket

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// ErrNotRepo is returned when the directory is not a git repository.
var ErrNotRepo = errors.New("not a git repository")

// WorkspaceStatusReport holds the parsed git status of a workspace.
type WorkspaceStatusReport struct {
	// Branch is the current branch name.
	Branch string
	// Staged is the number of staged files.
	Staged int
	// Modified is the number of modified files.
	Modified int
	// Untracked is the number of untracked files.
	Untracked int
	// Clean indicates whether the working tree is clean.
	Clean bool
}

// Runner executes commands and returns stdout and an error.
type Runner interface {
	// Run executes a command in the given directory with the provided arguments.
	Run(dir string, args []string) ([]byte, error)
}

// Status returns the workspace status by running git status via the provided Runner.
func Status(dir string, runner Runner) (WorkspaceStatusReport, error) {
	var report WorkspaceStatusReport
	out, err := runner.Run(dir, []string{"status", "--porcelain", "-b"})
	if err != nil {
		if bytes.Contains(out, []byte("not a git repository")) {
			return report, ErrNotRepo
		}
		return report, fmt.Errorf("runner failed: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			branchInfo := line[3:]
			parts := strings.Split(branchInfo, "...")
			report.Branch = parts[0]
			report.Branch = strings.Split(report.Branch, " ")[0]
		} else if strings.HasPrefix(line, "?? ") {
			report.Untracked++
		} else if len(line) >= 2 {
			x := line[0]
			y := line[1]
			if x != ' ' && x != '?' {
				report.Staged++
			}
			if y != ' ' && y != '?' {
				report.Modified++
			}
		}
	}

	if report.Staged == 0 && report.Modified == 0 && report.Untracked == 0 {
		report.Clean = true
	}

	return report, nil
}
