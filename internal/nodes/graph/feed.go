// Package graph provides node feed utilities.
package graph

// NodeSnapshot represents the current state of a node.
type NodeSnapshot struct {
	State    string
	Load     float64
	Slots    int
	Memory   float64
	Disk     float64
	Warnings []string
}

// FeedEntry is the unified feed entry for a node with six fields.
type FeedEntry struct {
	State    string   `json:"state"`
	Load     float64  `json:"load"`
	Slots    int      `json:"slots"`
	Memory   float64  `json:"memory"`
	Disk     float64  `json:"disk"`
	Warnings []string `json:"warnings"`
}

// BuildFeedEntry creates a FeedEntry from a NodeSnapshot.
// Warnings are capped at 5 entries, keeping the first 5 in order.
func BuildFeedEntry(s NodeSnapshot) FeedEntry {
	w := s.Warnings
	if w == nil {
		w = []string{}
	}
	if len(w) > 5 {
		w = w[:5]
	}
	return FeedEntry{
		State:    s.State,
		Load:     s.Load,
		Slots:    s.Slots,
		Memory:   s.Memory,
		Disk:     s.Disk,
		Warnings: w,
	}
}
