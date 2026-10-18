package conversation

// Purpose: the conversation domain's table names, schema version and portable
//   migration set, split out of domain.go to keep both files under the
//   300-line cap. Behaviour-neutral move: the declarations below are
//   verbatim from domain.go.
// SPORT: internal.conversation.schema/ADDED.

import "github.com/acamarata/cascade/internal/storage/migrate"

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
// the marker tables archive.go/retention.go add below; 8->9 by
// P1-E20-W5-S44-T2 for conversation_thread_privacy (privacy.go); 9->10 for
// conversation_turn_origin (origin.go).
const conversationSchemaVersion = 10

// SchemaVersion is conversationSchemaVersion exported for a future
// composition root's reader-ceiling max(), matching registry/jobs's own
// precedent. Not yet wired -- see testonly-allow.json's entry for
// ApplyConversationSchema.
const SchemaVersion = conversationSchemaVersion

// text/id/num/fk/uniqueIdx moved to archive.go under Art.10.3's 300-line cap.

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
			privacyStep(),
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
			originStep(),
		},
	}
}
