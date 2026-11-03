package learn

// Purpose: the learn-routing schema: four tables in the jobs domain under
//   their own SetID "learn-routing", so they never collide with the "learn"
//   set P1-CAP-02/-03 owns or the "learn-config" set P1-LRN-01 owns.
// Inputs: none (Go MigrationStep literals, never a numbered .sql file).
// Outputs: RoutingMigrationSet, applied by migrate.Apply at daemon start.
// Constraints: no free-text column (the model-id refusal leg is P1-LRN-07c);
//   no promotion column (topology owns lane_class); no changes to
//   jobs_scheduler_decision* tables.
// SPORT: domain:jobs/jobs_lane_posterior, jobs_family_pair,
//   jobs_folded_marker_inbound, jobs_folded_marker_outbound
//   (SetID learn-routing v1, P1-LRN-07).

import "github.com/acamarata/cascade/internal/storage/migrate"

// The four routing tables (jobs_ prefix, jobs domain).
const (
	tableLanePosterior        = "jobs_lane_posterior"
	tableFamilyPair           = "jobs_family_pair"
	tableFoldedMarkerInbound  = "jobs_folded_marker_inbound"
	tableFoldedMarkerOutbound = "jobs_folded_marker_outbound"
	learnRoutingSetID         = "learn-routing"
	learnRoutingSchemaLevel   = 1
)

// RoutingMigrationSet is the routing schema, SetID "learn-routing",
// SchemaVersion 1.
func RoutingMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         learnRoutingSetID,
		SchemaVersion: learnRoutingSchemaLevel,
		ReaderCeiling: learnRoutingSchemaLevel,
		Steps: []migrate.MigrationStep{
			lanePosteriorTableStep(),
			familyPairTableStep(),
			foldedMarkerInboundTableStep(),
			foldedMarkerOutboundTableStep(),
		},
	}
}

// routingText builds a NOT NULL TEXT column.
func routingText(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeText, NotNull: true}
}

// routingInteger builds a NOT NULL INTEGER column.
func routingInteger(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeInteger, NotNull: true}
}

// lanePosteriorTableStep: one row per lane with its posterior probability
// distribution over task classes.
func lanePosteriorTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_lane_posterior: posterior distribution per lane over task classes",
		Table: &migrate.TableDef{
			Name: tableLanePosterior,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				routingText("lane_id"),
				routingText("task_class"),
				{Name: "alpha", Type: migrate.TypeReal, NotNull: true},
				{Name: "beta", Type: migrate.TypeReal, NotNull: true},
				routingInteger("observation_count"),
				routingInteger("last_updated"),
			},
		},
	}
}

// familyPairTableStep: one row per (parent_lane, child_lane) pair with
// observed transition statistics.
func familyPairTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_family_pair: transition statistics between parent and child lanes",
		Table: &migrate.TableDef{
			Name: tableFamilyPair,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				routingText("parent_lane_id"),
				routingText("child_lane_id"),
				routingInteger("transition_count"),
				routingInteger("success_count"),
				routingInteger("failure_count"),
				routingInteger("last_observed_at"),
			},
		},
	}
}

// foldedMarkerInboundTableStep: one row per inbound folded marker observation.
func foldedMarkerInboundTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_folded_marker_inbound: inbound folded marker observations",
		Table: &migrate.TableDef{
			Name: tableFoldedMarkerInbound,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				routingText("lane_id"),
				routingText("marker_hash"),
				routingInteger("first_seen_at"),
				routingInteger("last_seen_at"),
				routingInteger("observation_count"),
			},
		},
	}
}

// foldedMarkerOutboundTableStep: one row per outbound folded marker emission.
func foldedMarkerOutboundTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_folded_marker_outbound: outbound folded marker emissions",
		Table: &migrate.TableDef{
			Name: tableFoldedMarkerOutbound,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				routingText("lane_id"),
				routingText("marker_hash"),
				routingInteger("emitted_at"),
				routingInteger("ttl_seconds"),
			},
		},
	}
}
