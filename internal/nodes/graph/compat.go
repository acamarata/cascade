package graph

// Node represents a node in the graph with its supported protocol version range.
type Node struct {
	ID       string
	ProtoMin int
	ProtoMax int
}

// IsIncompatible returns true if the node's protocol range is exactly 9..9,
// indicating it is not compatible with the current system.
func IsIncompatible(n Node) bool {
	return n.ProtoMin == 9 && n.ProtoMax == 9
}

// Schedulable filters out incompatible nodes from the given list and returns
// only the nodes that are safe to schedule.
func Schedulable(nodes []Node) []Node {
	var res []Node
	for _, n := range nodes {
		if !IsIncompatible(n) {
			res = append(res, n)
		}
	}
	return res
}
