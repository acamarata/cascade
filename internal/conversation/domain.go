package conversation

// Purpose: the conversation domain's three immutable record types (Thread,
//   Turn, Segment), their closed vocabularies (Role, SegmentKind), and the
//   conversation_thread/conversation_turn/conversation_segment schema,
//   authored through the B/S-02.T3 portable migration builder
//   (internal/storage/migrate), matching internal/jobs/migration.go's and
//   internal/providers/registry/migration.go's exact precedent.
//
// SCHEMA VERSION: MigrationSet's own SetID ("conversation") keeps its
// schema_version independent of every other set's (ledger key: SetID +
// schema_version, R-16.77) -- the fix that unblocked daemon wiring.
//
// DOMAIN REGISTRATION and RAW-SQL MIGRATION FILE: two contract/tree
// contradictions, both quoted in full in this ticket's journal. Summary:
// internal/storage/domains.go's AllDomains is CLOSED at eleven members
// and this ticket's files_scope.change is empty, so this package does
// NOT claim a DomainID -- table-prefixed "conversation_" instead, matching
// internal/providers/registry.MigrationSet's identical situation. And no
// package in this tree authors an executable raw-SQL migration (the DSL
// below is); migrations/001_conversation_tables.sql is a checked-in
// human-readable reference only, never imported or executed.
//
// PRIVACY (this ticket's own hard rule): Segment.Content carries free-text
// conversation user data and is stored/read back verbatim but is NEVER
// interpolated into a log line, error message, or metric label anywhere in
// this package -- see errors.go and conversation_test.go's
// TestErrorsNeverEchoContent for the enforced proof.
//
// SPORT: internal.conversation.domain/ADDED (P1-E20-W5-S43-T1).

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock abstracts time.Now so domain logic never reads the wall clock
// directly (forbidigo, A-T2). Declared locally, duck-typed, matching
// internal/storage/domains.go's and internal/storage/migrate.Clock's own
// precedent: any concrete Clock in the tree (internal/runtime's,
// internal/testkit's) already satisfies this with zero adapter code.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// Role is the closed set of participants a Turn may be attributed to.
// The zero value is deliberately not a member (fail-closed default),
// matching internal/jobs.ConsequenceClass's identical pattern.
type Role string

// The closed Role vocabulary.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Valid reports whether r is one of the four closed values.
func (r Role) Valid() bool {
	switch r {
	case RoleUser, RoleAssistant, RoleSystem, RoleTool:
		return true
	}
	return false
}

// DecodeRole parses a stored TEXT value into a Role. An unknown or empty
// value returns a typed error -- there is no permissive zero value; a
// corrupt or unparseable stored role is refused, never silently accepted
// as one of the four.
func DecodeRole(raw string) (Role, error) {
	r := Role(raw)
	if !r.Valid() {
		return "", cascade.Newf(cascade.KindIntegrity,
			"conversation: unknown role %q: must be one of user, assistant, system, tool", raw)
	}
	return r, nil
}

// SegmentKind is the closed set of a Segment's content shapes.
type SegmentKind string

// The closed SegmentKind vocabulary.
const (
	SegmentText       SegmentKind = "text"
	SegmentCode       SegmentKind = "code"
	SegmentToolCall   SegmentKind = "tool_call"
	SegmentToolResult SegmentKind = "tool_result"
)

// Valid reports whether k is one of the four closed values.
func (k SegmentKind) Valid() bool {
	switch k {
	case SegmentText, SegmentCode, SegmentToolCall, SegmentToolResult:
		return true
	}
	return false
}

// DecodeSegmentKind parses a stored TEXT value into a SegmentKind. An
// unknown or empty value returns a typed error -- fail-closed, never a
// permissive default kind.
func DecodeSegmentKind(raw string) (SegmentKind, error) {
	k := SegmentKind(raw)
	if !k.Valid() {
		return "", cascade.Newf(cascade.KindIntegrity,
			"conversation: unknown segment kind %q: must be one of text, code, tool_call, tool_result", raw)
	}
	return k, nil
}

// Thread is a named sequence of Turns. Immutable once created: its row is
// written exactly once (AppendTurn's INSERT OR IGNORE on first use), and
// no field is ever updated afterward -- there is deliberately no stored
// "updated_at" column, since that would be a mutation of an existing row
// every subsequent append would have to perform. A caller that needs the
// most recent activity instant computes it from ListTurns's own
// CreatedAt values.
type Thread struct {
	ID        string
	Name      string
	CreatedAt int64 // unix seconds, injected Clock
}

// Turn is one exchange within a Thread, attributed to a Role. Turn.ID is
// content-addressed (see NewTurnID) and Turn.Seq is the turn's 0-based
// position within its thread -- both are immutable once appended.
type Turn struct {
	ID        string
	ThreadID  string
	Seq       int64
	Role      Role
	CreatedAt int64 // unix seconds, injected Clock
}

// Segment is a typed slice of a Turn's content. Segment.ID is
// content-addressed and Segment.Seq is the segment's 0-based position
// within its turn -- both immutable once appended. Content carries
// conversation user data and is never interpolated into a log line, an
// error message, or a metric label anywhere in this package (see this
// file's PRIVACY doc comment).
type Segment struct {
	ID        string
	TurnID    string
	Seq       int64
	Kind      SegmentKind
	Content   string
	CreatedAt int64 // unix seconds, injected Clock
}

// NewTurnID returns the content-addressed, deterministic id for a turn at
// position seq within threadID, attributed to role: same inputs always
// yield the same id, which is what lets AppendTurn detect a duplicate via
// the DB's own PRIMARY KEY rather than a second, advisory-only check.
func NewTurnID(threadID string, seq int64, role Role) string {
	return contentAddress("turn", threadID, itoa(seq), string(role))
}

// NewSegmentID is NewTurnID's twin for segments. Deliberately excludes
// Content from the hash: two segments at the same (turnID, seq, kind) are
// the same segment by construction, and hashing free-text content into an
// id that reaches logs/metrics elsewhere would be the leak PRIVACY forbids.
func NewSegmentID(turnID string, seq int64, kind SegmentKind) string {
	return contentAddress("segment", turnID, itoa(seq), string(kind))
}

// contentAddress hashes its parts (each length-prefixed so "ab"+"c" and
// "a"+"bc" never collide) with BLAKE3, matching internal/memory.HashBody's
// precedent: pure Go, no CGO, no randomness, same digest for the same
// input on every platform and run.
func contentAddress(parts ...string) string {
	h := blake3.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(itoa(int64(len(p)))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// itoa is strconv.FormatInt, named locally so contentAddress's call sites
// read as plain string-building rather than a strconv import at every
// call site.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Table names, prefixed "conversation_" so they cannot collide with any
// of the eleven registered cascade.db domains' own TablePrefix values
// (see this file's DOMAIN REGISTRATION doc comment).
const (
	tableThread  = "conversation_thread"
	tableTurn    = "conversation_turn"
	tableSegment = "conversation_segment"
)

// conversationSchemaVersion is this package's MigrationSet target
// version -- the next unused slot in the single global sequence. See this
// file's SCHEMA VERSION doc comment. Bumped 7->8 by P1-E20-W5-S44-T3 for
// the two new marker tables archive.go/retention.go's MigrationSet steps
// add below (conversation_thread_archive, conversation_turn_tombstone).
const conversationSchemaVersion = 8

// SchemaVersion is conversationSchemaVersion exported for a future
// composition root's reader-ceiling max(), matching registry/jobs's own
// precedent. Not yet wired -- see testonly-allow.json's entry for
// ApplyConversationSchema.
const SchemaVersion = conversationSchemaVersion

// text/id/num/fk/uniqueIdx moved to archive.go under Art.10.3's 300-line
// cap once this ticket's two new table steps landed -- same behavior.

// MigrationSet is the conversation domain's five-table schema: the three
// from S-43.T1 plus conversation_thread_archive/conversation_turn_tombstone
// (archive.go/retention.go -- NEW tables, not ALTER TABLE; the migrate DSL
// has no ALTER step, see search.go's identical FTS5 note). The two
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "conversation",
		SchemaVersion: conversationSchemaVersion,
		ReaderCeiling: conversationSchemaVersion,
		Steps: []migrate.MigrationStep{
			{Kind: migrate.StepCreateTable, Description: "conversation_thread", Table: &migrate.TableDef{
				Name: tableThread, Columns: []migrate.ColumnDef{id("id"), text("name"), num("created_at")},
			}},
			{Kind: migrate.StepCreateTable, Description: "conversation_turn: append-only", Table: &migrate.TableDef{
				Name:        tableTurn,
				Columns:     []migrate.ColumnDef{id("id"), text("thread_id"), num("seq"), text("role"), num("created_at")},
				ForeignKeys: []migrate.ForeignKeyDef{fk("thread_id", tableThread)},
			}},
			uniqueIdx("idx_conversation_turn_thread_seq", tableTurn, "thread_id", "seq"),
			{Kind: migrate.StepCreateTable, Description: "conversation_segment: append-only", Table: &migrate.TableDef{
				Name:        tableSegment,
				Columns:     []migrate.ColumnDef{id("id"), text("turn_id"), num("seq"), text("kind"), text("content"), num("created_at")},
				ForeignKeys: []migrate.ForeignKeyDef{fk("turn_id", tableTurn)},
			}},
			uniqueIdx("idx_conversation_segment_turn_seq", tableSegment, "turn_id", "seq"),
			{Kind: migrate.StepCreateTable, Description: "conversation_thread_archive: archived-state marker (R-14.90)", Table: &migrate.TableDef{
				Name:        tableThreadArchive,
				Columns:     []migrate.ColumnDef{id("thread_id"), num("archived_at")},
				ForeignKeys: []migrate.ForeignKeyDef{fk("thread_id", tableThread)},
			}},
			{Kind: migrate.StepCreateTable, Description: "conversation_turn_tombstone: logical-prune marker", Table: &migrate.TableDef{
				Name:        tableTurnTombstone,
				Columns:     []migrate.ColumnDef{id("turn_id"), num("tombstoned_at")},
				ForeignKeys: []migrate.ForeignKeyDef{fk("turn_id", tableTurn)},
			}},
		},
	}
}

// ApplyConversationSchema idempotently applies MigrationSet against db,
// then ensureFTS5 (search.go) -- a raw DDL statement the typed migrate
// DSL cannot express (no virtual-table step kind exists), issued outside
// the ledger but still idempotent via its own IF NOT EXISTS. dbPath/
// backupDir enable migrate's SQLite snapshot when non-empty.
func ApplyConversationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "conversation: ApplyConversationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "conversation: ApplyConversationSchema requires a non-nil Clock")
	}
	if err := migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, MigrationSet()); err != nil {
		return err
	}
	return ensureFTS5(ctx, db, dialect)
}

// bootstrapResume is the daemon-startup entry point for P1-E20-W5-S44-T4's
// journal integration (journal.go): for every thread id in threadIDs it
// calls Resume against js/store and sums the applied counts, so a caller
// resuming after a crash restores every in-progress thread's state in one
// call rather than looping Resume itself. Transparent to callers per this
// ticket's own contract: a thread whose journal has nothing unresolved
// contributes 0 and is not itself an error. The caller supplies
// threadIDs (e.g. from a durable thread-id list this composition root
// already tracks) rather than this function discovering them, since
// conversation.Store has no ListThreadIDs-shaped call cheaper than
// ListThreads -- see store.go's own Store interface.
func bootstrapResume(ctx context.Context, js JournalStore, store Store, threadIDs []string) (applied int, err error) {
	for _, threadID := range threadIDs {
		n, err := Resume(ctx, js, store, threadID)
		if err != nil {
			return applied, err
		}
		applied += n
	}
	return applied, nil
}
