package background

// Backlog represents a collection of items to be processed.
type Backlog struct {
	Items []Item
}

// Item represents a single entry in a backlog.
type Item struct {
	Title    string
	Evidence string
}

// Finding represents a single observation or issue discovered during a health check.
type Finding struct {
	Summary  string
	Evidence string
}

// HealthReading aggregates findings from a health check.
type HealthReading struct {
	Findings []Finding
}

// OnEmptyBacklog inspects a backlog. If the backlog already contains items,
// it returns a zero Item and false without mutating the backlog or invoking
// the logger. The health readings and logs are provided for downstream use
// when the backlog is genuinely empty.
func OnEmptyBacklog(b *Backlog, _ HealthReading, logs []string, logf func(string)) (Item, bool) {
	if len(b.Items) > 0 {
		return Item{}, false
	}
	// For an empty backlog, no specific addition logic is required by this
	// ticket beyond skipping when items already exist.
	return Item{}, false
}
