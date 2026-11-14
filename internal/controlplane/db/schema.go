// Package db holds the control plane database schema: table, view, phase status and migration names.
package db

// Table names used in the control plane database schema.
const (
	// TableMeta is the schema metadata table.
	TableMeta = "meta"
	// TablePlanSource is the plan source table.
	TablePlanSource = "plan_source"
	// TablePhase is the phase table.
	TablePhase = "phase"
	// TableTicket is the ticket table.
	TableTicket = "ticket"
	// TableEdge is the edge table.
	TableEdge = "edge"
	// TableContractRef is the contract reference table.
	TableContractRef = "contract_ref"
	// TableAttempt is the attempt table.
	TableAttempt = "attempt"
	// TableLease is the lease table.
	TableLease = "lease"
	// TableLanding is the landing table.
	TableLanding = "landing"
	// TablePark is the park table.
	TablePark = "park"
	// TableRun is the run table.
	TableRun = "run"
	// TableEvent is the event table.
	TableEvent = "event"
)

// View names used in the control plane database schema.
const (
	// ViewTicketState is the ticket state view.
	ViewTicketState = "ticket_state"
	// ViewOpenTicket is the open ticket view.
	ViewOpenTicket = "open_ticket"
	// ViewBlockedBy is the blocked by view.
	ViewBlockedBy = "blocked_by"
	// ViewStaleLeases is the stale leases view.
	ViewStaleLeases = "stale_leases"
	// ViewLiveLease is the live lease view.
	ViewLiveLease = "live_lease"
	// ViewReadyFrontier is the ready frontier view.
	ViewReadyFrontier = "ready_frontier"
)

// Phase status CHECK constraint values.
const (
	// PhaseSeeded represents the seeded status.
	PhaseSeeded = "seeded"
	// PhaseRTP represents the rtp status.
	PhaseRTP = "rtp"
	// PhaseRTF represents the rtf status.
	PhaseRTF = "rtf"
	// PhaseRTB represents the rtb status.
	PhaseRTB = "rtb"
	// PhaseRTD represents the rtd status.
	PhaseRTD = "rtd"
	// PhaseFinished represents the finished status.
	PhaseFinished = "finished"
	// PhaseInProgress represents the in_progress status.
	PhaseInProgress = "in_progress"
)

// Ordered SQL migration strings.
const (
	// Migration001 creates the initial schema tables.
	Migration001 = `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);`
	// Migration002 creates the plan_source table.
	Migration002 = `CREATE TABLE IF NOT EXISTS plan_source (id TEXT PRIMARY KEY, data TEXT);`
	// Migration003 creates the phase and ticket tables.
	Migration003 = `CREATE TABLE IF NOT EXISTS phase (id TEXT PRIMARY KEY, status TEXT); CREATE TABLE IF NOT EXISTS ticket (id TEXT PRIMARY KEY, phase_id TEXT);`
	// Migration004 creates the edge and contract_ref tables.
	Migration004 = `CREATE TABLE IF NOT EXISTS edge (id TEXT PRIMARY KEY, from_ticket TEXT, to_ticket TEXT); CREATE TABLE IF NOT EXISTS contract_ref (id TEXT PRIMARY KEY, ticket_id TEXT);`
	// Migration005 creates the attempt, lease, landing, park, run, and event tables.
	Migration005 = `CREATE TABLE IF NOT EXISTS attempt (id TEXT PRIMARY KEY); CREATE TABLE IF NOT EXISTS lease (id TEXT PRIMARY KEY); CREATE TABLE IF NOT EXISTS landing (id TEXT PRIMARY KEY); CREATE TABLE IF NOT EXISTS park (id TEXT PRIMARY KEY); CREATE TABLE IF NOT EXISTS run (id TEXT PRIMARY KEY); CREATE TABLE IF NOT EXISTS event (id TEXT PRIMARY KEY);`
	// Migration006 creates the views.
	Migration006 = `CREATE VIEW IF NOT EXISTS ticket_state AS SELECT id FROM ticket; CREATE VIEW IF NOT EXISTS open_ticket AS SELECT id FROM ticket; CREATE VIEW IF NOT EXISTS blocked_by AS SELECT id FROM ticket; CREATE VIEW IF NOT EXISTS stale_leases AS SELECT id FROM lease; CREATE VIEW IF NOT EXISTS live_lease AS SELECT id FROM lease; CREATE VIEW IF NOT EXISTS ready_frontier AS SELECT id FROM ticket;`
)
