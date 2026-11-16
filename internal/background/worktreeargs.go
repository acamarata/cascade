// Package background provides utilities for background task management.
package background

import (
	"fmt"
)

// Ticket represents a gateway swarm ticket that may edit files in a lane.
type Ticket struct {
	ID        string
	Lane      string
	Prompt    string
	EditsLane bool
	Files     []string
}

// WorktreeArgs builds the arguments for creating a worktree for the given
// ticket. When the ticket edits an unmerged lane, --base lane/<x> is included
// so the worktree sees the lane's files. Otherwise no base flag is added.
// Returns an error if EditsLane is true but Lane is empty.
func WorktreeArgs(t Ticket, unmerged map[string]bool) ([]string, error) {
	if t.EditsLane && t.Lane == "" {
		return nil, fmt.Errorf("lane name is empty but EditsLane is true")
	}

	args := []string{"worktree", "create", "--ticket", t.ID}

	if t.EditsLane && unmerged[t.Lane] {
		args = append(args, "--base", "lane/"+t.Lane)
	}

	return args, nil
}
