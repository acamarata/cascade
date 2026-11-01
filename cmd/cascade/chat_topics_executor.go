// Purpose: the topic engine's classifier executor and its process
//
//	collaborators (P1-CPA-12): chatModelExecutor dispatches every classify
//	request in-process through daemon.ProcessConductorDoor().Execute at the
//	thread's own tier, and chatTopicDepsFor gathers the [topics] settings,
//	the process embedder Selection, the door and the classifier lane's
//	locality lookup.
//
// Inputs: a ctx the topic engine stamped with the thread tier
//
//	(conductor.ContextWithDoorSensitivity); the daemon's config path.
//
// Outputs: provider.ModelResponse, or a typed refusal made before any call.
//
// Constraints: a request with no stamped tier is refused, never sent at a
//
//	guessed one. The door has no lane pin: the router picks the lane under
//	the thread's privacy (FILTER 0) and the stamped tier, so the engine's
//	classifier-lane admission check (chat_topics_engine.go) is a
//	precondition, and the router's own table is the backstop.
//
// SPORT: cmd/cascade chat topics executor (ADD, P1-CPA-12).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/daemon"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// chatModelExecutor is the segmenter's provider.ModelExecutor: the
// in-process conductor door, at the tier stamped on each call's ctx.
type chatModelExecutor struct {
	door  provider.ModelExecutor
	lane  string
	model string
}

// Execute stamps req.Sensitivity with the thread tier on ctx and
// dispatches through the door. A ctx without a valid tier is refused with
// KindPolicyDenied and no call.
func (e chatModelExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	tier, ok := conductor.DoorSensitivityFrom(ctx)
	if !ok || !tier.Valid() {
		return provider.ModelResponse{}, cascade.Newf(cascade.KindPolicyDenied,
			"topics: classifier lane %q (model %q): no thread tier on the request; refusing to classify", e.lane, e.model)
	}
	if e.door == nil {
		return provider.ModelResponse{}, cascade.New(cascade.KindUnavailable, "topics: no conductor door")
	}
	req.Sensitivity = tier
	req.Policy.ExternalAllowed = conductor.SensitivityAllows(tier, conductor.LaneExternalAPI, false)
	return e.door.Execute(ctx, req)
}

// laneTypeFunc reports the locality of the named lane.
type laneTypeFunc func(ctx context.Context, lane string) (conductor.LaneType, error)

// chatTopicDeps are the topic engine's process collaborators.
type chatTopicDeps struct {
	settings    runtime.TopicsSettings
	settingsErr error
	selection   *embedsel.Selection
	door        provider.ModelExecutor
	laneType    laneTypeFunc
	retrieval   provider.Store
	storeReason string
	// taxonomy maps classifier labels to topic types. Core ships no labels
	// (R-14.63): the zero value resolves every label to the empty type,
	// which the topics package refuses, until a plugin injects its own.
	taxonomy topics.TaxonomyConfig
}

// chatTopicStoreReason names why the production composition has no
// retrieval provider.Store for the exemplar and observe-state rows yet.
const chatTopicStoreReason = "the daemon's provider.Store is not threaded into wireChatTopics " +
	"(cmd/cascade/chat_wiring.go passes only the conversation store)"

// chatTopicDepsFor gathers the production collaborators for paths: the
// [topics] settings, the SAME Selection the recall vector leg reads
// (processEmbedder), the process conductor door and the registry lookup.
func chatTopicDepsFor(paths runtime.PathProvider, clock runtime.Clock) chatTopicDeps {
	deps := chatTopicDeps{
		selection:   processEmbedder(paths),
		door:        daemon.ProcessConductorDoor(),
		laneType:    registryLaneType(paths, clock),
		storeReason: chatTopicStoreReason,
		taxonomy:    topics.NewTaxonomyConfig(nil, ""), // MECHANISM ONLY: core names no labels (R-14.63)
	}
	if paths == nil {
		return deps
	}
	cfg, err := loadPolicyConfig(context.Background(), paths)
	if err != nil {
		deps.settingsErr = err
		return deps
	}
	deps.settings, deps.settingsErr = runtime.TopicsSettingsFrom(cfg)
	return deps
}

// registryLaneType classifies a lane from the providers registry: the
// lane by name from ListLanes, then its provider's locality through
// conductor.ClassifyLane, the table FILTER 0 and FILTER 2 read.
func registryLaneType(paths runtime.PathProvider, clock runtime.Clock) laneTypeFunc {
	return func(ctx context.Context, lane string) (conductor.LaneType, error) {
		if paths == nil {
			return conductor.LaneUnresolved, cascade.New(cascade.KindUnavailable, "topics: no data directory")
		}
		dbPath := filepath.Join(paths.DataDir(), providerRegistryDBFile)
		db, err := openMigratedDB(ctx, dbPath, func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
		if err != nil {
			return conductor.LaneUnresolved, err
		}
		defer func() { _ = db.Close() }()
		reader := providerregistry.NewReader(providerregistry.NewRegistry(db, clock))
		return classifyNamedLane(ctx, reader, lane)
	}
}

// classifyNamedLane finds lane in reader and classifies its provider. An
// unknown lane is KindNotFound.
func classifyNamedLane(ctx context.Context, reader provider.ProviderRegistryReader, lane string) (conductor.LaneType, error) {
	lanes, err := reader.ListLanes(ctx)
	if err != nil {
		return conductor.LaneUnresolved, cascade.Wrap(cascade.KindUnavailable, err, "topics: list lanes")
	}
	for _, l := range lanes {
		if l.LaneName != lane {
			continue
		}
		info, err := reader.GetProvider(ctx, l.ProviderName)
		if err != nil {
			return conductor.LaneUnresolved, err
		}
		return conductor.ClassifyLane(info), nil
	}
	return conductor.LaneUnresolved, cascade.Newf(cascade.KindNotFound, "topics: no lane named %q", lane)
}
