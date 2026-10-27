// Purpose: the one constructor every production policy.Engine in this binary
//
//	is built through, and the two non-daemon builders over it (the github-ci
//	engine here, the MCP engine in mcp_tools.go). The daemon (wirePolicy), the MCP
//	tool filter and the github-ci verbs each used to assemble their own engine:
//	only the daemon's read the stored deny list and the operator's [policy]
//	profile, so a deny row in the policy store never refused an MCP tool, and a
//	stricter profile never reached MCP or github-ci. The merge-on-green request
//	has no action text, so the github-ci engine matches it as githubCIMergeActions.
//
// Inputs: a policyEngineInputs -- the runtime store, the clock, the loaded
//
//	config (nil means no config file), the capability set the caller routes, the
//	path provider the audit log's vault custody is bound to, and an optional bus.
//
// Outputs: a policyEngineParts -- the engine and every collaborator it was
//
//	built over, the one redacting audit log (openAuditLog) and same-turn ledger.
//
// Constraints: untagged (the MCP and github-ci callers run on every OS).
//
//	Nothing here installs a permissive default: a malformed [policy] section, an
//	unreadable deny store, a store or clock that is absent and an audit log that
//	cannot be opened are each returned as an error, never as a partially wired
//	engine that would answer by omission. Approval signing is not built here.
//
// SPORT: cmd/cascade/policy (ADD) -- the policy engine constructor.
package main

import (
	"context"
	"os"
	"reflect"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// policyEngineInputs is everything newPolicyEngine builds from.
type policyEngineInputs struct {
	// Store is the runtime store the grants and deny rows live in.
	Store provider.Store
	// Clock is the process clock the grant store expires against.
	Clock runtime.Clock
	// Config is the loaded config the [policy] profile is read from. Nil
	// means no config file: the shipped default profile applies.
	Config *runtime.Config
	// Capabilities is the set the caller routes; an unregistered capability
	// denies, so each call site names its own.
	Capabilities []policy.Capability
	// Paths binds the audit log's vault custody to a data directory.
	Paths runtime.PathProvider
	// Bus receives the audit log's append events; nil is allowed.
	Bus audit.Publisher
}

// policyEngineParts is what newPolicyEngine built, named as a set so a caller
// cannot take the engine without the collaborators that answer for it.
type policyEngineParts struct {
	// Engine is the policy engine over everything below.
	Engine *policy.Engine
	// Registry holds Capabilities.
	Registry *policy.MemoryRegistry
	// Grants is the durable grant store.
	Grants *policy.StoreGrants
	// DenyList is read once; MCP and github-ci re-read per decision.
	DenyList *policy.StoreDenyList
	// Controller carries the operator's autonomy profile.
	Controller *policy.Controller
	// Audit is the process's one redacting audit log for Store.
	Audit *audit.Log
	// SameTurn is the same-turn ledger; only the daemon attaches it (wire_policy_authorize.go).
	SameTurn *policy.SameTurnLedger
}

// newPolicyEngine builds the grant store, the stored deny list, the operator autonomy
// profile, the same-turn ledger and the redacting audit log, and the engine over them.
// A nil Store or Clock is KindInvalidInput; a malformed [policy] section is the
// applyAutonomyProfile / validateRiskGateOverlay error. The deny list is read here,
// once, so an unreadable or corrupt deny store refuses the construction.
func newPolicyEngine(ctx context.Context, in policyEngineInputs) (policyEngineParts, error) {
	if isNilStore(in.Store) {
		return policyEngineParts{}, cascade.New(cascade.KindInvalidInput, "policy engine: a store is required")
	}
	if in.Clock == nil {
		return policyEngineParts{}, cascade.New(cascade.KindInvalidInput, "policy engine: a clock is required")
	}
	registry := policy.NewMemoryRegistry()
	for _, capability := range in.Capabilities {
		if err := registry.Add(ctx, capability); err != nil {
			return policyEngineParts{}, err
		}
	}
	grants, err := policy.NewStoreGrants(in.Store, registry, in.Clock)
	if err != nil {
		return policyEngineParts{}, err
	}
	denyList, err := policy.NewStoreDenyList(in.Store)
	if err != nil {
		return policyEngineParts{}, err
	}
	if _, err := denyList.List(ctx); err != nil {
		return policyEngineParts{}, err
	}
	controller := policy.NewController(nil)
	if err := applyAutonomyProfile(ctx, controller, in.Config); err != nil {
		return policyEngineParts{}, err
	}
	engine, err := policy.NewEngine(registry, grants, controller)
	if err != nil {
		return policyEngineParts{}, err
	}
	engine = engine.WithDenyList(denyList)
	sameTurn, err := policy.NewSameTurnLedger(in.Clock)
	if err != nil {
		return policyEngineParts{}, err
	}
	log, err := openAuditLog(in.Store, in.Clock, in.Bus, in.Paths)
	if err != nil {
		return policyEngineParts{}, err
	}
	return policyEngineParts{
		Engine: engine, Registry: registry, Grants: grants,
		DenyList: denyList, Controller: controller, Audit: log, SameTurn: sameTurn,
	}, nil
}

// loadPolicyConfig reads config.toml read-only for a process that does not own
// the config (the MCP stdio server, the github-ci verbs). A missing file is the
// default profile; a file that does not parse, or cannot be followed (see
// refuseUnfollowableConfig), is an error the caller must turn into a refusal.
func loadPolicyConfig(ctx context.Context, paths runtime.PathProvider) (*runtime.Config, error) {
	if err := refuseUnfollowableConfig(paths); err != nil {
		return nil, err
	}
	return runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath()})
}

// refuseUnfollowableConfig refuses a config.toml that exists but cannot be
// followed or opened (a symlink to nothing, an unreadable file): runtime.Load
// reads "cannot resolve" as "no config file", which would silently select the
// default profile. An absent entry passes. A same-user race between this check
// and Load can only reach the default profile an absent file would have.
func refuseUnfollowableConfig(paths runtime.PathProvider) error {
	if paths == nil || paths.ConfigPath() == "" {
		return cascade.New(cascade.KindInvalidInput, "policy engine: no config.toml path to read the [policy] profile from")
	}
	path := paths.ConfigPath()
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "policy engine: config.toml exists but cannot be read; refusing rather than using the default profile")
	}
	return nil
}

// freshDenyList is the deny list for a process that outlives the rows it was started
// with (the MCP stdio server, a github-ci verb that waits on CI). StoreDenyList caches
// the rows it reads; this reads the store on every decision, so the answer never
// depends on a cache this process does not own. A read failure is returned, which
// the engine treats as listed (fail closed).
//
// commandless is the set of commands a request with an empty action is checked as:
// a row is a command pattern and the deny list has no opinion on an empty action,
// so without it no row could refuse the merge-on-green request. Any one form a row
// names denies. Empty means no substitution.
type freshDenyList struct {
	store       provider.Store
	commandless []string
}

// githubCIMergeActions are the commands the action-less merge-on-green
// request is matched against deny rows as: the bare command, the command with
// an argument (a "gh pr merge *" row needs one) and one per merge method. A row
// naming a PR number or repository cannot match: the request has no such text.
var githubCIMergeActions = []string{
	"gh pr merge", "gh pr merge --auto", "gh pr merge --squash", "gh pr merge --merge", "gh pr merge --rebase",
}

// Denied implements policy.DenyLister over a freshly read deny list.
func (f freshDenyList) Denied(ctx context.Context, action string) (bool, error) {
	list, err := policy.NewStoreDenyList(f.store)
	if err != nil {
		return false, err
	}
	if action != "" || len(f.commandless) == 0 {
		return list.Denied(ctx, action)
	}
	for _, form := range f.commandless {
		denied, err := list.Denied(ctx, form)
		if err != nil || denied {
			return denied, err
		}
	}
	return false, nil
}

// withFreshDenyList points the engine at the store's current deny rows for
// every decision, for the non-daemon callers. newPolicyEngine has already
// proved the store's deny rows readable, so this only adds freshness.
//
// commandless is the freshDenyList substitution for a request with no action
// text; the MCP caller passes nil (its requests all carry one).
func withFreshDenyList(parts policyEngineParts, store provider.Store, commandless []string) *policy.Engine {
	return parts.Engine.WithDenyList(freshDenyList{store: store, commandless: commandless})
}

// applyAutonomyProfile loads the [policy] section into the controller. A nil config
// carries no section, which Apply resolves to the shipped baseline; a malformed
// section is returned as an error rather than warned past, because unlike a
// maintenance schedule an unreadable policy section decides what the daemon may do.
//
// [policy.risk_gates]'s gate-step names are validated HERE, before Apply, rather
// than inside internal/policy: that package cannot import internal/jobs (an import
// cycle -- see risk_gates_config.go's own doc comment), so its own parse only checks
// the overlay's shape and its risk-class-name keys. This composition root imports
// both packages, so a bad gate-step name refuses the whole reload before
// controller.Apply swaps anything in (validate-before-write, R-16.70(b), AH/S-69.T1).
func applyAutonomyProfile(ctx context.Context, controller *policy.Controller, cfg *runtime.Config) error {
	if cfg == nil {
		return controller.Apply(ctx, map[string]interface{}{})
	}
	if err := validateRiskGateOverlay(cfg); err != nil {
		return err
	}
	return controller.Apply(ctx, cfg.Extra)
}

// validateRiskGateOverlay re-parses the [policy] section far enough to reach
// RiskGates and runs jobs.BuildRiskGateOverlay over it, so an unparseable gate-step
// name is caught before the reload swap rather than only when a later `policy risk
// explain` call happens to hit it. A [policy] section this file cannot even parse
// structurally is not this function's error to report -- controller.Apply reports
// that one itself, with the richer refusal-audit path Controller already owns.
func validateRiskGateOverlay(cfg *runtime.Config) error {
	parsed, err := policy.ParseConfig(cfg.Extra)
	if err != nil {
		return nil
	}
	_, err = jobs.BuildRiskGateOverlay(parsed.RiskGates)
	return err
}

// githubCIPolicy builds the real policy engine and audit log an unattended
// merge is decided by, over the runtime store. It reuses openMCPPolicyStore,
// this binary's ONE platform-split opener for the runtime store, so the grants
// an operator wrote with `cascade policy grant` are the ones read here.
func githubCIPolicy() (*policy.Engine, audit.Writer, func(), error) {
	return buildGitHubCIPolicy(context.Background(), lazyPaths{}, runtime.NewSystemClock())
}

// buildGitHubCIPolicy is githubCIPolicy over explicit paths and clock: it
// opens the runtime store and builds the engine over it.
func buildGitHubCIPolicy(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*policy.Engine, audit.Writer, func(), error) {
	store, closeStore, err := openMCPPolicyStore(paths, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	engine, log, err := githubCIEngine(ctx, store, clock, paths)
	if err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	return engine, log, closeStore, nil
}

// githubCIEngine is the github-ci engine and audit log over an opened store,
// from the one policy constructor, so the stored deny list and the operator's
// [policy] profile apply to the merge as they do to the daemon. The deny rows
// are re-read on every decision (see freshDenyList). A config.toml that does
// not parse is returned as an error; it never degrades to the default profile.
func githubCIEngine(ctx context.Context, store provider.Store, clock runtime.Clock, paths runtime.PathProvider) (*policy.Engine, audit.Writer, error) {
	cfg, err := loadPolicyConfig(ctx, paths)
	if err != nil {
		return nil, nil, err
	}
	parts, err := newPolicyEngine(ctx, policyEngineInputs{
		Store: store, Clock: clock, Config: cfg, Capabilities: githubCICapabilities(), Paths: paths,
	})
	if err != nil {
		return nil, nil, err
	}
	return withFreshDenyList(parts, store, githubCIMergeActions), parts.Audit, nil
}

// isNilStore reports a nil interface or an interface holding a nil pointer;
// the latter passes a plain == nil check and would panic at the first read.
func isNilStore(store provider.Store) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
