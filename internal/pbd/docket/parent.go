package docket

// Node represents a node in a hierarchy.
type Node struct {
	ID     string
	Parent string
	Shell  bool
}

// Parent returns the nearest ancestor of the node with the given ID, skipping
// over any shell nodes. It searches the provided slice of nodes. If no
// non-shell ancestor exists, or the node is not found, it returns "".
func Parent(nodes []Node, id string) string {
	nodeMap := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	curr, ok := nodeMap[id]
	if !ok {
		return ""
	}

	for {
		if curr.Parent == "" {
			return ""
		}
		parentNode, ok := nodeMap[curr.Parent]
		if !ok {
			return ""
		}
		if !parentNode.Shell {
			return parentNode.ID
		}
		curr = parentNode
	}
}
