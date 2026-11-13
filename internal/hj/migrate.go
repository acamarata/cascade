package hj

import "sort"

// Node represents a compute node with a certain number of free slots.
type Node struct {
	Name      string
	FreeSlots int
}

// Task represents a task assigned to a node, which may be queued or running.
type Task struct {
	ID      string
	Node    string
	Running bool
	Age     int
}

// Move represents a migration of a queued task from one node to another.
type Move struct {
	TaskID string
	From   string
	To     string
}

// Rebalance migrates queued tasks to nodes with open capacity.
// Running tasks never move. Tasks are migrated oldest first (Age descending, stable).
func Rebalance(nodes []Node, queue []Task) []Move {
	freeSlots := make(map[string]int)
	for _, n := range nodes {
		freeSlots[n.Name] = n.FreeSlots
	}

	sortedQueue := make([]Task, len(queue))
	copy(sortedQueue, queue)
	sort.SliceStable(sortedQueue, func(i, j int) bool {
		return sortedQueue[i].Age > sortedQueue[j].Age
	})

	var moves []Move
	for _, t := range sortedQueue {
		if t.Running {
			continue
		}
		for _, n := range nodes {
			if n.Name != t.Node && freeSlots[n.Name] > 0 {
				moves = append(moves, Move{
					TaskID: t.ID,
					From:   t.Node,
					To:     n.Name,
				})
				freeSlots[n.Name]--
				break
			}
		}
	}

	return moves
}
