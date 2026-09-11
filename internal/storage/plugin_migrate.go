package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: PluginMigrator — the B/S-02.T3 migration-builder adapter
// backing PluginStorage.Migrate (plugin.go), per this ticket's task 4.
//
// Tree correction (full contradiction quoted in this ticket's journal):
// the contract says Migrate should "stamp schema_version ... per B/S-02.T3
// contract" as if each caller owned an independent version sequence. It
// does not — internal/jobs/migration.go's own R-14.198 finding is that
// applied_migrations keys schema_version GLOBALLY across cascade.db, one
// sequence shared by every migrate.Apply caller (bootstrap=1, scope=2,
// lifecycle=3, registry=4, jobs=5). Compile-time packages coordinate by
// grepping `SchemaVersion:`; a RUNTIME-installed plugin cannot. This file
// resolves that: pluginMigrationState (a small JSON record, stored per
// plugin) tracks the plugin-local Version last applied and the GLOBAL
// slot it landed under. A call whose highest Version is already recorded
// is the idempotent no-op path (no ledger read, no DDL, no write).
// Otherwise this file claims the NEXT global slot via MAX(schema_version)
// at call time (never a compile-time constant) and calls migrate.Apply
// under it, then records the plugin-local -> global mapping.
//
// SPORT: internal.storage.PluginMigrator/ADDED (P1-E15-W4-S32-T3).

// pluginMigrationStateKey is the reserved "__"-prefixed key, inside a
// plugin's own domain, holding its pluginMigrationState JSON record.
const pluginMigrationStateKey = "__migration_state__"

// pluginMigrationState records how far one plugin's schema has migrated
// (PluginVersion) and which global ledger slot it landed under.
type pluginMigrationState struct {
	PluginVersion int `json:"plugin_version"`
	GlobalVersion int `json:"global_version"`
}

// PluginMigrator is the real Migrator (plugin.go) implementation, backed
// by a shared *sql.DB and the B/S-02.T3 migration builder.
type PluginMigrator struct {
	db        *sql.DB
	dialect   migrate.Dialect
	clock     migrate.Clock
	dbPath    string
	backupDir string
	store     stateStore
}

// stateStore is the minimal provider.Store slice PluginMigrator needs
// (local, so tests supply a trivial fake with no unrelated methods).
type stateStore interface {
	Get(ctx context.Context, namespace, key string) ([]byte, error)
	Put(ctx context.Context, namespace, key string, value []byte) error
}

// NewPluginMigrator builds a PluginMigrator. db/dialect/clock/store are
// required; dbPath/backupDir are both-or-neither (§D-18 snapshot).
func NewPluginMigrator(db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string, store stateStore) (*PluginMigrator, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginMigrator requires a non-nil db")
	}
	if dialect == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginMigrator requires a non-nil Dialect")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginMigrator requires a non-nil Clock")
	}
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginMigrator requires a non-nil state store")
	}
	if dbPath != "" && backupDir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginMigrator backupDir is required when dbPath is set")
	}
	return &PluginMigrator{db: db, dialect: dialect, clock: clock, dbPath: dbPath, backupDir: backupDir, store: store}, nil
}

// Apply implements Migrator; see the package doc comment for the
// global-slot indirection.
func (m *PluginMigrator) Apply(ctx context.Context, pluginID string, migrations []plugin.Migration) (plugin.MigrationReport, error) {
	if err := validatePluginID(pluginID); err != nil {
		return plugin.MigrationReport{}, err
	}
	sorted := sortedMigrations(migrations)
	if err := validateMigrationVersions(sorted); err != nil {
		return plugin.MigrationReport{}, err
	}

	state, err := m.loadState(ctx, pluginID)
	if err != nil {
		return plugin.MigrationReport{}, err
	}

	pending := pendingMigrations(sorted, state.PluginVersion)
	if len(pending) == 0 {
		return plugin.MigrationReport{AlreadyCurrent: true, CurrentVersion: state.PluginVersion}, nil
	}

	applied := make([]int, 0, len(pending))
	for _, mig := range pending {
		globalVersion, verr := m.applyOne(ctx, pluginID, mig)
		if verr != nil {
			return plugin.MigrationReport{}, verr
		}
		state.PluginVersion = mig.Version
		state.GlobalVersion = globalVersion
		if serr := m.saveState(ctx, pluginID, state); serr != nil {
			return plugin.MigrationReport{}, serr
		}
		applied = append(applied, mig.Version)
	}

	return plugin.MigrationReport{AppliedVersions: applied, CurrentVersion: state.PluginVersion}, nil
}

// applyOne claims the next global slot and applies mig's steps under it.
func (m *PluginMigrator) applyOne(ctx context.Context, pluginID string, mig plugin.Migration) (int, error) {
	dialect := m.dialect
	if mig.PortabilityPostgresOnly {
		dialect = migrate.PostgresEmitter{}
	}

	nextGlobal, err := m.nextGlobalVersion(ctx)
	if err != nil {
		return 0, err
	}

	set := migrate.MigrationSet{
		SchemaVersion:        nextGlobal,
		MinimumReaderVersion: nextGlobal,
		Steps:                make([]migrate.MigrationStep, 0, len(mig.Steps)),
	}
	for _, step := range mig.Steps {
		table, terr := convertTableDef(pluginID, step.Table)
		if terr != nil {
			return 0, terr
		}
		set.Steps = append(set.Steps, migrate.MigrationStep{
			Kind:        migrate.StepCreateTable,
			Table:       table,
			Description: step.Description,
		})
	}

	if err := migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        m.db,
		Dialect:   dialect,
		Clock:     m.clock,
		DBPath:    m.dbPath,
		BackupDir: m.backupDir,
	}, set); err != nil {
		return 0, err
	}
	return nextGlobal, nil
}

// nextGlobalVersion reads MAX(schema_version) and returns one past it.
func (m *PluginMigrator) nextGlobalVersion(ctx context.Context) (int, error) {
	row := m.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(schema_version), 0) FROM `+bootstrapLedgerTable)
	var maxVersion int
	if err := row.Scan(&maxVersion); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "storage: plugin migrate: reading current ledger version")
	}
	return maxVersion + 1, nil
}

func (m *PluginMigrator) loadState(ctx context.Context, pluginID string) (pluginMigrationState, error) {
	raw, err := m.store.Get(ctx, pluginNamespace(pluginID), pluginMigrationStateKey)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
			return pluginMigrationState{}, nil
		}
		return pluginMigrationState{}, err
	}
	var state pluginMigrationState
	if err := json.Unmarshal(raw, &state); err != nil {
		return pluginMigrationState{}, cascade.Wrap(cascade.KindIntegrity, err, "storage: plugin migrate: decoding migration state")
	}
	return state, nil
}

func (m *PluginMigrator) saveState(ctx context.Context, pluginID string, state pluginMigrationState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "storage: plugin migrate: encoding migration state")
	}
	return m.store.Put(ctx, pluginNamespace(pluginID), pluginMigrationStateKey, encoded)
}

// sortedMigrations returns migrations sorted ascending by Version.
func sortedMigrations(migrations []plugin.Migration) []plugin.Migration {
	out := make([]plugin.Migration, len(migrations))
	copy(out, migrations)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// validateMigrationVersions refuses an empty/non-positive/duplicate
// version before any DDL runs.
func validateMigrationVersions(sorted []plugin.Migration) error {
	if len(sorted) == 0 {
		return cascade.New(cascade.KindInvalidInput, "storage: plugin migrate: no migrations supplied")
	}
	seen := make(map[int]bool, len(sorted))
	for _, mig := range sorted {
		if mig.Version < 1 {
			return cascade.Newf(cascade.KindInvalidInput, "storage: plugin migrate: version must be >= 1, got %d", mig.Version)
		}
		if seen[mig.Version] {
			return cascade.Newf(cascade.KindInvalidInput, "storage: plugin migrate: duplicate version %d", mig.Version)
		}
		seen[mig.Version] = true
		if len(mig.Steps) == 0 {
			return cascade.Newf(cascade.KindInvalidInput, "storage: plugin migrate: version %d has no steps", mig.Version)
		}
	}
	return nil
}

// pendingMigrations returns entries whose Version exceeds currentVersion.
func pendingMigrations(sorted []plugin.Migration, currentVersion int) []plugin.Migration {
	var pending []plugin.Migration
	for _, mig := range sorted {
		if mig.Version > currentVersion {
			pending = append(pending, mig)
		}
	}
	return pending
}

// convertTableDef maps a pkg/plugin.TableDef onto migrate.TableDef,
// namespacing under the plugin id ("plugin_<id_>_<name>") so two plugins
// never collide. Plugin ids are kebab-case; migrate's identifierPattern
// forbids hyphens, so foldHyphens maps them to "_" (always identifier-safe:
// kebab-case yields only [a-z0-9_], a strict subset of migrate's allow-list).
func convertTableDef(pluginID string, table plugin.TableDef) (*migrate.TableDef, error) {
	if table.Name == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: plugin migrate: table has no name")
	}
	if len(table.Columns) == 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "storage: plugin migrate: table %q has no columns", table.Name)
	}
	cols := make([]migrate.ColumnDef, 0, len(table.Columns))
	for _, c := range table.Columns {
		colType, err := convertColumnType(c.Type)
		if err != nil {
			return nil, err
		}
		cols = append(cols, migrate.ColumnDef{
			Name:          c.Name,
			Type:          colType,
			PrimaryKey:    c.PrimaryKey,
			AutoIncrement: c.AutoIncrement,
			NotNull:       c.NotNull,
		})
	}
	return &migrate.TableDef{
		Name:    pluginTableName(pluginID, table.Name),
		Columns: cols,
	}, nil
}

// pluginTableName builds a namespaced, identifier-safe table name.
func pluginTableName(pluginID, tableName string) string {
	return "plugin_" + foldHyphens(pluginID) + "_" + tableName
}

// foldHyphens replaces "-" with "_" (see convertTableDef).
func foldHyphens(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			out[i] = '_'
		} else {
			out[i] = s[i]
		}
	}
	return string(out)
}

// convertColumnType maps plugin.ColumnType onto migrate.ColumnType. No
// default case: exhaustive-lint fails the build on a future unhandled value.
func convertColumnType(t plugin.ColumnType) (migrate.ColumnType, error) {
	switch t {
	case plugin.ColumnText:
		return migrate.TypeText, nil
	case plugin.ColumnInteger:
		return migrate.TypeInteger, nil
	case plugin.ColumnBlob:
		return migrate.TypeBlob, nil
	}
	return 0, cascade.Newf(cascade.KindInvalidInput, "storage: plugin migrate: unknown column type %d", t)
}
