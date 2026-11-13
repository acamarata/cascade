// Package docket provides ticket event indexing.
package docket

import "sort"

// Event represents a ticket event.
type Event struct {
	Ticket string
	Kind   string
	Time   int64
}

// Index is an in-memory index for ticket events keyed by ticket, kind, and time.
type Index struct {
	events map[string]map[string][]Event
}

// NewIndex creates a new Index.
func NewIndex() *Index {
	return &Index{
		events: make(map[string]map[string][]Event),
	}
}

// Add adds an event to the index. It ignores an event equal on all fields.
func (i *Index) Add(e Event) {
	if i.events[e.Ticket] == nil {
		i.events[e.Ticket] = make(map[string][]Event)
	}
	evts := i.events[e.Ticket][e.Kind]
	for _, existing := range evts {
		if existing.Ticket == e.Ticket && existing.Kind == e.Kind && existing.Time == e.Time {
			return
		}
	}
	i.events[e.Ticket][e.Kind] = append(evts, e)
}

// Lookup returns matching events sorted by time for a given ticket and kind.
// Returns an empty slice if no match is found.
func (i *Index) Lookup(ticket, kind string) []Event {
	if i.events[ticket] == nil {
		return []Event{}
	}
	evts := i.events[ticket][kind]
	if len(evts) == 0 {
		return []Event{}
	}

	// Copy to avoid mutating the slice in the index when sorting
	result := make([]Event, len(evts))
	copy(result, evts)

	sort.Slice(result, func(a, b int) bool {
		return result[a].Time < result[b].Time
	})

	return result
}
