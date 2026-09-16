// Purpose: the daemon composition root's dispatch wiring — the controller
//   entry point (node.dispatch) and the two verbs the NODE calls back over
//   its own tunnel (node.dispatch.claim / node.dispatch.report), all over
//   one process-lifetime fencing register.
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider and
//   runtime.Clock.
// Outputs: the three verbs mounted over a real internal/nodes dispatch
//   engine, closing the same wiring gap node_upgrade_rpc.go closed for
//   node.upgrade: every symbol below had a test caller only until this file.
// Constraints: the fencing register and the rendezvous are built ONCE, here,
//   and shared by all three verbs. That is the whole point of the file: a
//   per-call register hands out attempt 1 every time, so nothing ever
//   supersedes anything and ErrStaleAttempt can never fire — fencing would
//   be present in the code and absent in fact.
//
//   The git remote and repository root are read from the [nodes] config
//   section per call rather than captured at registration, matching
//   node_upgrade_rpc.go's identical per-call resolution: a dispatch needs a
//   live remote, and binding it at daemon start would leave the verb broken
//   for the rest of the process's life if it moved. A dispatch against an
//   unconfigured remote is REFUSED outright (fail closed), never silently
//   run against the controller's own checkout.
// SPORT: internal/daemon (ADD, P1-E17-W4-S37-T2).

package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// execGitRunner runs the real git binary.
//
// It lives in this package rather than in internal/nodes because
// internal/nodes is NOT on the os/exec allowlist and must not become the
// tree's second git-spawning site — internal/nodes holds the git SEMANTICS
// (branch and worktree naming, the push/fetch legs) behind the GitRunner
// interface, and a permitted package does the spawning.
type execGitRunner struct{}

// Run executes git with args in dir and returns its stdout.
func (execGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), cascade.Wrapf(cascade.KindUnavailable, err,
			"daemon: git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// RegisterNodeDispatchHandlers mounts the dispatch verbs on registry and
// returns the shared dispatcher and the rendezvous the node reports
// through. The dispatcher is returned so the journal verb can be mounted
// over the SAME fencing register (RegisterNodeDispatchJournal); fencing a
// streamed record against a different register than the one that minted
// its attempt would refuse every record.
//
// Elevation is enforced by internal/rpc's shared ElevationMiddleware over
// the canonical elevationTable, exactly as every sibling registerXHandler
// in this package leaves it.
func RegisterNodeDispatchHandlers(
	registry *rpc.Registry,
	records *nodes.RecordStore,
	clock runtime.Clock,
	config func() (nodes.Section, error),
) (*nodes.Dispatcher, *nodes.Rendezvous) {
	dispatcher := nodes.NewDispatcher()
	rendezvous := nodes.NewRendezvous()

	nodes.RegisterDispatchHandler(registry, func(_ context.Context, nodeID string) (nodes.ShipDeps, nodes.DeviceRecord, error) {
		return resolveDispatchDeps(dispatcher, rendezvous, records, clock, config, nodeID)
	})
	nodes.RegisterDispatchNodeHandlers(registry, rendezvous)
	return dispatcher, rendezvous
}

// resolveDispatchDeps builds one dispatch's dependencies.
func resolveDispatchDeps(
	dispatcher *nodes.Dispatcher,
	rendezvous *nodes.Rendezvous,
	records *nodes.RecordStore,
	clock runtime.Clock,
	config func() (nodes.Section, error),
	nodeID string,
) (nodes.ShipDeps, nodes.DeviceRecord, error) {
	if records == nil || config == nil {
		return nodes.ShipDeps{}, nodes.DeviceRecord{}, cascade.New(cascade.KindInternal,
			"daemon: the dispatch path is not wired to a record store and a config reader")
	}
	cfg, err := config()
	if err != nil {
		return nodes.ShipDeps{}, nodes.DeviceRecord{}, err
	}
	if err := requireDispatchConfig(cfg); err != nil {
		return nodes.ShipDeps{}, nodes.DeviceRecord{}, err
	}
	record, err := records.Get(nodeID)
	if err != nil {
		return nodes.ShipDeps{}, nodes.DeviceRecord{}, err
	}
	deps := dispatcher.ShipDepsFor(cfg.DispatchRepoRoot, cfg.DispatchRemote, execGitRunner{}, rendezvous, dispatchClock{clock})
	return deps, record, nil
}

// requireDispatchConfig refuses an unconfigured dispatch outright.
//
// Falling back to the controller's own checkout would run remote work
// against the operator's working tree, which is the one outcome a remote
// dispatch must never produce silently.
func requireDispatchConfig(cfg nodes.Section) error {
	for field, value := range map[string]string{
		"dispatch_repo_root": cfg.DispatchRepoRoot,
		"dispatch_remote":    cfg.DispatchRemote,
	} {
		if strings.TrimSpace(value) == "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"daemon: remote dispatch needs [nodes].%s configured", field)
		}
	}
	return nil
}

// dispatchClock adapts the daemon's runtime.Clock to nodes.Clock.
type dispatchClock struct{ clock runtime.Clock }

// Now reports the current time from the daemon's injected clock, never
// from time.Now — an attempt stamped by an unspecified clock is an attempt
// whose ordering cannot be reproduced (Art.7.3).
func (c dispatchClock) Now() time.Time { return c.clock.Now() }

// resolveNodesSection parses the [nodes] table out of cfg.
func resolveNodesSection(cfg *runtime.Config) (nodes.Section, error) {
	return nodes.LoadSection(mapSection(cfg.Extra["nodes"]))
}

// mapSection narrows one runtime.Config.Extra entry to a config table, or
// nil when the section is absent or is not a table at all.
//
// A non-table is handed on as nil rather than refused here: the section's
// own parser owns what a malformed table means, and a second reader making
// that judgement is how two readers drift apart.
func mapSection(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// journalStreamSink appends a node's streamed record to the controller's
// entity journal as a KindNodeStream entry.
//
// KindNodeStream is the kind R-21.216's CLOSED enumeration already reserves
// for exactly this, so no entry kind is added here — the taxonomy stays
// frozen and `cascade fleet journal show|replay` reads these back with
// every other kind.
type journalStreamSink struct{ store *journal.SQLiteStore }

// AppendNodeStream appends one admitted record.
func (s journalStreamSink) AppendNodeStream(ctx context.Context, entityID, operationID string, payload json.RawMessage) error {
	if s.store == nil {
		return cascade.New(cascade.KindInternal, "daemon: no journal store is open for the dispatch stream")
	}
	_, err := s.store.Append(ctx, entityID, journal.KindNodeStream, operationID, payload)
	return err
}

// RegisterNodeDispatchJournal mounts the journal-stream verb over store,
// fenced against the same register the ship leg mints attempts from.
func RegisterNodeDispatchJournal(registry *rpc.Registry, d *nodes.Dispatcher, store *journal.SQLiteStore) {
	nodes.RegisterDispatchJournalHandler(registry, d.JournalDepsFor(journalStreamSink{store: store}))
}
