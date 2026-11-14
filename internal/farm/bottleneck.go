package farm

// LimitKind represents the category of a bottleneck limit.
type LimitKind int

const (
	// GC_RPM_Headroom represents the garbage collection requests-per-minute headroom limit.
	GC_RPM_Headroom LimitKind = iota
	// GC_Stream_Ceiling represents the garbage collection stream ceiling limit.
	GC_Stream_Ceiling
	// Node_Load represents the node load limit.
	Node_Load
	// Swap represents the swap usage limit.
	Swap
	// Account_Quota_Left represents the remaining account quota limit.
	Account_Quota_Left
	// Local_Model_RAM represents the local model RAM limit.
	Local_Model_RAM
	// Queue_Wait represents the queue wait time limit.
	Queue_Wait
)

// Evidence holds the raw data sources used to derive bottleneck metrics.
// It contains read-only references to the usage log table and broker snapshot.
type Evidence struct {
	// UsageLogTable holds the usage log table data.
	UsageLogTable interface{}
	// BrokerSnapshot holds the broker snapshot data.
	BrokerSnapshot interface{}
}

// BottleneckRow represents a single metric contributing to a bottleneck report.
type BottleneckRow struct {
	// Kind is the category of the limit being reported.
	Kind LimitKind
	// Value is the numeric measurement of the limit.
	Value float64
	// Unit is the unit of measurement for the value (e.g., "req/min", "MB").
	Unit string
	// Source indicates where the measurement originated.
	Source string
	// Measured indicates whether the value is a direct measurement rather than an estimate.
	Measured bool
}

// BottleneckReport wraps a collection of BottleneckRow entries to provide
// a read-only view of current system bottlenecks. It never changes settings
// or sends data.
type BottleneckReport struct {
	// Rows is the slice of bottleneck metrics.
	Rows []BottleneckRow
}
