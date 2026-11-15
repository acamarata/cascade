// Package landing declares the data shapes and seams used to land a batch
// of work items: the Batch and Item records, the Journal that remembers
// which items have landed, and the Conductor that lands a single item.
package landing

// Batch is a named group of items that land together.
type Batch struct {
	// ID identifies the batch.
	ID string
	// Items are the work items in the batch, in landing order.
	Items []Item
	// State is the batch lifecycle state.
	State string
}

// Item is one unit of work inside a Batch.
type Item struct {
	// Key identifies the item within its batch.
	Key string
	// Payload is the opaque item content.
	Payload []byte
	// Landed reports whether the item has already landed.
	Landed bool
}
