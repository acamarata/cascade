// Package frontier provides chain memoisation and longest-path computation.
package frontier

import "errors"

// ErrCycleDetected is returned when a dependency cycle is found.
var ErrCycleDetected = errors.New("cycle detected")

// ChainMemo caches the longest-path lengths for tickets.
type ChainMemo struct {
	depths map[string]int
}

// NewChainMemo creates a new, empty ChainMemo.
func NewChainMemo() ChainMemo {
	return ChainMemo{depths: make(map[string]int)}
}

// Get retrieves the cached depth for a ticket.
func (m ChainMemo) Get(id string) (int, bool) {
	if m.depths == nil {
		return 0, false
	}
	d, ok := m.depths[id]
	return d, ok
}

// LongestPath returns the longest remaining path from ticketID through its dependencies.
// It detects cycles by tracking visited nodes in the current traversal stack.
func LongestPath(ticketID string, edges map[string][]string, memo ChainMemo) (int, error) {
	visited := make(map[string]bool)
	return dfs(ticketID, edges, memo, visited)
}

func dfs(id string, edges map[string][]string, memo ChainMemo, visited map[string]bool) (int, error) {
	if d, ok := memo.Get(id); ok {
		return d, nil
	}
	if visited[id] {
		return 0, ErrCycleDetected
	}
	visited[id] = true

	maxDepth := 0
	for _, dep := range edges[id] {
		d, err := dfs(dep, edges, memo, visited)
		if err != nil {
			return 0, err
		}
		if d > maxDepth {
			maxDepth = d
		}
	}

	visited[id] = false
	depth := maxDepth + 1
	memo.depths[id] = depth
	return depth, nil
}
