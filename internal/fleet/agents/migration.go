package agents

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// Migrate applies the fleet-agents migration set to the given database.
// It creates the sessions_agents table and is fully idempotent.
func Migrate(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock) error {
	set := agentsMigrationSet()
	cfg := migrate.ApplyConfig{
		DB:      db,
		Dialect: dialect,
		Clock:   clock,
	}
	return migrate.Apply(ctx, cfg, set)
}

func agentsMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "fleet-agents",
		SchemaVersion: 1,
		ReaderCeiling: 1,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "create sessions_agents table",
				Table: &migrate.TableDef{
					Name: "sessions_agents",
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeText, PrimaryKey: true},
						{Name: "state", Type: migrate.TypeText, NotNull: true},
					},
				},
			},
		},
	}
}
