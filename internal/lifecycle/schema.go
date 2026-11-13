package lifecycle

import (
	"database/sql"
)

// ProjectCfg stores configuration for a project.
type ProjectCfg struct {
	// Mode must be "shadow" or "apply".
	Mode string
	// DisplayName is the human-readable name of the project.
	DisplayName string
	// RepoPath is the filesystem path to the repository.
	RepoPath string
	// ChatContext holds contextual information for chat interactions.
	ChatContext string
}

// PhaseState tracks the current phase of a project.
type PhaseState struct {
	// Phase is the current phase name.
	Phase string
}

// Decision records a decision made during a transition.
type Decision struct {
	// ConsultedModel is the name of the model consulted for the decision.
	ConsultedModel string
	// DefaultChosen indicates if the default option was selected.
	DefaultChosen bool
	// Reason explains why the decision was made.
	Reason string
}

// Authorization records an authorization event.
type Authorization struct {
	// GrantedBy identifies who granted the authorization.
	GrantedBy string
}

// Step records an execution step.
type Step struct {
	// Action is the name or description of the action performed.
	Action string
	// Result is the outcome of the action.
	Result string
}

// OwnerQueue records items waiting for owner attention.
type OwnerQueue struct {
	// Phase is the phase associated with the queue item.
	Phase string
	// Kind categorizes the queue item.
	Kind string
	// Subject is the specific subject of the queue item.
	Subject string
}

// Touchpoint records interactions.
type Touchpoint struct {
	// Actor is the entity performing the interaction.
	Actor string
	// Note contains details about the interaction.
	Note string
}

// Wait records waiting periods.
type Wait struct {
	// Reason describes why the wait is occurring.
	Reason string
}

// Hunt records hunt state for crash safety.
type Hunt struct {
	// Target is the subject being hunted.
	Target string
	// State is the current state of the hunt.
	State string
}

// Meta stores arbitrary metadata.
type Meta struct {
	// Key is the metadata key.
	Key string
	// Value is the metadata value.
	Value string
}

const migrationProjectCfg = `CREATE TABLE IF NOT EXISTS project_cfg (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	mode TEXT NOT NULL,
	display_name TEXT NOT NULL,
	repo_path TEXT NOT NULL,
	chat_context TEXT NOT NULL
);`

const migrationPhaseState = `CREATE TABLE IF NOT EXISTS phase_state (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	phase TEXT NOT NULL
);`

const migrationTransition = `CREATE TABLE IF NOT EXISTS transition (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	who TEXT NOT NULL,
	why TEXT NOT NULL,
	shadow BOOLEAN NOT NULL
);`

const migrationDecision = `CREATE TABLE IF NOT EXISTS decision (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	consulted_model TEXT NOT NULL,
	default_chosen BOOLEAN NOT NULL,
	reason TEXT NOT NULL
);`

const migrationAuthorization = `CREATE TABLE IF NOT EXISTS authorization (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	granted_by TEXT NOT NULL
);`

const migrationStep = `CREATE TABLE IF NOT EXISTS step (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	action TEXT NOT NULL,
	result TEXT NOT NULL
);`

const migrationOwnerQueue = `CREATE TABLE IF NOT EXISTS owner_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	phase TEXT NOT NULL,
	kind TEXT NOT NULL,
	subject TEXT NOT NULL,
	UNIQUE(phase, kind, subject)
);`

const migrationTouchpoint = `CREATE TABLE IF NOT EXISTS touchpoint (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor TEXT NOT NULL,
	note TEXT NOT NULL
);`

const migrationWait = `CREATE TABLE IF NOT EXISTS wait (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	reason TEXT NOT NULL
);`

const migrationHunt = `CREATE TABLE IF NOT EXISTS hunt (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	target TEXT NOT NULL,
	state TEXT NOT NULL
);`

const migrationMeta = `CREATE TABLE IF NOT EXISTS meta (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	key TEXT NOT NULL,
	value TEXT NOT NULL
);`

var migrations = []string{
	migrationProjectCfg,
	migrationPhaseState,
	migrationTransition,
	migrationDecision,
	migrationAuthorization,
	migrationStep,
	migrationOwnerQueue,
	migrationTouchpoint,
	migrationWait,
	migrationHunt,
	migrationMeta,
}

// runMigrations executes all schema migrations sequentially on the given database connection.
func runMigrations(db *sql.DB) error {
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return err
		}
	}
	return nil
}
