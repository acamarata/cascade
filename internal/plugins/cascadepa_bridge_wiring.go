package plugins

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose (this file): the production composition root for Epic W's Telegram
//   bridge — the ONE place the module's stores, its bot token, its egress
//   firewall, its journal and its pairing-code key are assembled, handed to the
//   DAEMON as a subsystem, and given a working issuance path.
//
// WHERE THE POLL LOOP LIVES, AND WHY THAT IS THE WHOLE POINT. The module runs
//   in the daemon, started under the daemon's own context and stopped by its
//   shutdown drain (internal/daemon/subsystem_bridge.go, mounted from
//   cmd/cascade/plugin_rpc.go). It used to be started from `cascade pa pair`,
//   whose context ends when that command returns — so the long poll died before
//   its first 30-second getUpdates came back, and "/pair <code>" typed into
//   Telegram reached nothing at all. Issuance now goes the other way: the CLI
//   asks the daemon for a code over pa.pair_code (see
//   cascadepa_bridge_client.go), so the process that mints a code is the process
//   that verifies it and the pairing-code key never has to leave it.
//
// Inputs: BridgeDeps — a data directory, the custody selection the composition
//   root made, a clock and the daemon's event bus. Nothing is read at import
//   time; every path, vault read and manifest read happens in this call.
//
// Outputs: a BridgeRuntime of bare funcs the daemon mounts: Start, Stop and
//   IssueCode.
//
// Constraints, each a decision rather than a default:
//   - DEFAULT OFF, TWICE. The poll starts only when the cascade-pa module
//     manifest says telegram.enabled=true AND the vault key resolves through
//     real custody under a standing grant. Neither an environment variable nor
//     a config key can enable it, and the shipped manifest says false. A
//     disabled bridge is a DisabledReason, never an error: the daemon must
//     still start.
//   - ISSUANCE NEEDS THE BRIDGE'S OWN CREDENTIAL. The pairing code is stored
//     as an HMAC digest keyed from the bot token (R-16.37 pins the code's
//     shape, so the strength has to come from a key the database does not
//     hold), so a code can only be issued for a subject whose module is
//     actually configured. Any other subject is refused by name rather than
//     issued and silently unverifiable.
//   - THE HANDLERS ARE REGISTERED HERE. Until W/S-48.T2's chat route lands, an
//     admitted message reaches a handler that RECORDS it as unrouted; without
//     that, an admitted message was dropped inside the module with no trace
//     anywhere, which is indistinguishable from a refusal.
//   - THE REAL SECRET SCANNER IS BOUND HERE (T0 D1, P1-E23-W5-S48-T3).
//     bridgeSecretScanner wraps the SAME *secrets.Detector construction
//     dispatch.go:171 uses; without this wiring the module's own unwired
//     default REFUSES every message (cascadepa_bridge_deps.go, refuse.go) —
//     so a missing wiring line here is a fail-closed regression, not a
//     fail-open one.
//   - internal/plugins is the ONE package allowed to import both internal/**
//     and plugins/** (Art.10.2) — see cascadepa_tools_wiring.go's precedent.
//
// SPORT: internal/plugins:cascadepa-bridge-wiring (ADD) — P1-E23-W5-S48-T1.

// bridgeVaultKey is the vault entry the bot token is read from. A constant, so
// no caller can point the read at another secret.
const bridgeVaultKey = "cascade-pa.telegram.bot_token"

// BridgeVaultConfig is the custody selection the bridge's token read uses: the
// SAME service label `cascade vault` itself writes under, so a secret stored at
// the terminal is the secret the bridge resolves. The composition root calls
// this and passes the result in — a bridge that selected its own custody could
// not be exercised without reaching the operator's real keychain (R-14.206).
func BridgeVaultConfig(dataDir string) secrets.Config {
	return secrets.Config{Service: secrets.DefaultVaultService, Dir: dataDir}
}

// BridgePairCode is one issued pairing code, as the composition root hands it
// back to the daemon's RPC layer.
type BridgePairCode struct {
	Code      string
	Subject   string
	ExpiresAt time.Time
}

// BridgeDeps is everything NewCascadePABridge needs, all of it injected.
type BridgeDeps struct {
	// DataDir is the daemon's data directory: cascade.db, the module manifest
	// and the bridge's own device-record backend all live under it.
	DataDir string
	// Vault is the custody selection (BridgeVaultConfig in production, a
	// ForceFileVault config in tests). Required.
	Vault secrets.Config
	// Clock is the one clock every bridge store reads time from. Required.
	Clock cascadepa.PairClock
	// Events is the journal a lockout and an unrouted message are recorded on.
	// Required: a bridge whose lockouts go nowhere is the defect AC#16 names.
	Events BridgeEventPublisher
}

// BridgeRuntime is the assembled bridge, as bare funcs so internal/daemon can
// mount it without importing this package (that import is a real cycle:
// internal/plugins -> internal/client -> internal/daemon).
type BridgeRuntime struct {
	// Subject is the bridge instance's id (a bot-token digest), empty when no
	// module is enabled.
	Subject string
	// Start launches the long poll, or is nil when the bridge is not enabled.
	Start func(ctx context.Context) error
	// Stop cancels the poll and DRAINS it.
	Stop func(ctx context.Context) error
	// IssueCode backs pa.pair_code. Always non-nil: a disabled bridge answers
	// with a typed refusal that names what is missing, because a verb that
	// disappears when its feature is off cannot be diagnosed.
	IssueCode func(ctx context.Context, subject string) (BridgePairCode, error)
	// DisabledReason is what `cascade daemon status` reports when Start is nil.
	DisabledReason string
	// sink is the lockout/journal sink this wiring built and handed to the
	// module, kept so the wiring test can assert that production passes a
	// RECORDING sink and not the plugin's discarding default (AC#16). It is
	// evidence of one line of this file, not a collaborator anybody calls.
	sink telegram.LockoutSink
	// scanner is the SecretScanner this wiring built and handed to the
	// module, kept so the wiring test can assert that production passes a
	// REAL, detector-backed scanner and not the plugin's refusing-only
	// default (T0 D1, P1-E23-W5-S48-T3) — evidence, like sink above.
	scanner telegram.SecretScanner
	// Routes names the telegram handler kinds this wiring actually registered
	// on the module: HandlerText (the S-48.T2 chat bridge registers itself
	// inside NewCascadePABridgeChatHandler) and HandlerCallbackQuery (the
	// S-48.T4 approval handler registered right after it). It is the
	// wiring's own evidence: a module with no route drops every admitted
	// message inside itself, which nothing outside the process can see, so
	// "which routes did the composition root mount" has to be assertable.
	Routes []string
	// ApprovalBridge is the §5.24 producer leg (FIX-0); WireApprovalBridge arms it.
	ApprovalBridge policy.BridgeNotifier
}

// NewCascadePABridge assembles the bridge for deps.DataDir.
//
// It returns an error only for a genuine failure (a missing dependency, an
// unreadable data directory, a malformed manifest). "Not enabled" and "no token
// granted" are not failures: they produce a runtime with no Start, a
// DisabledReason the daemon reports, and an IssueCode that refuses by name.
func NewCascadePABridge(ctx context.Context, deps BridgeDeps) (*BridgeRuntime, error) {
	if deps.DataDir == "" || deps.Clock == nil || deps.Events == nil || deps.Vault.Service == "" {
		return nil, cascade.New(cascade.KindInvalidInput,
			"cascade-pa bridge: a data directory, a clock, a journal and a custody selection are all required")
	}
	cfg, err := readTelegramModuleConfig(deps.DataDir)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return disabledBridge("the telegram module is disabled in the cascade-pa module manifest"), nil
	}
	token, terr := resolveBridgeToken(ctx, deps.Vault, cfg.VaultKey)
	if terr != nil {
		// A configured-but-ungranted token is an operator state, not a broken
		// daemon: the reason is surfaced, the daemon starts, and `cascade pa
		// pair` says the same thing rather than issuing a useless code.
		return disabledBridge(terr.Error()), nil
	}
	return enabledBridge(ctx, deps, token)
}

// disabledBridge is the not-enabled runtime: no poll, and an issuance verb that
// refuses with the reason instead of vanishing.
func disabledBridge(reason string) *BridgeRuntime {
	refusal := cascade.Newf(cascade.KindUnavailable,
		"cascade pa pair: no bridge module is enabled, so there is no subject to pair (%s); "+
			"enable one in the cascade-pa module manifest and grant its bot token", reason)
	return &BridgeRuntime{
		DisabledReason: reason,
		IssueCode: func(context.Context, string) (BridgePairCode, error) {
			return BridgePairCode{}, refusal
		},
	}
}

// enabledBridge assembles the real thing: the durable state, the pairing-code
// key, the stores, the firewall, the journal, the module and its handlers.
func enabledBridge(ctx context.Context, deps BridgeDeps, token string) (*BridgeRuntime, error) {
	state, err := openBridgeState(ctx, deps.DataDir)
	if err != nil {
		return nil, err
	}
	pairKey, err := cascadepa.DerivePairCodeKey(token)
	if err != nil {
		return nil, closeStateOnError(state, err)
	}
	gate, err := newBridgeEgressGateFor(deps.Vault)
	if err != nil {
		return nil, closeStateOnError(state, err)
	}
	scanner, err := newBridgeSecretScanner()
	if err != nil {
		return nil, closeStateOnError(state, err)
	}
	stores := cascadepa.NewStores(deps.Clock, newBridgeDeviceRegistrar(deps.DataDir), state, pairKey)
	journal := newBridgeJournal(deps.Events)
	module := telegram.NewModule(token, nil, gate, bridgeElevationPolicy{}, stores, journal, scanner, journal)
	subject := telegram.SubjectFromToken(token)
	// FIX-0: the §5.24 producer leg (WireApprovalBridge arms it).
	approvalLeg, err := newApprovalBridgeLeg(subject, state, stores.Callback, module.Client(), deps.Clock)
	if err != nil {
		return nil, closeStateOnError(state, err)
	}
	// HandlerText: the S-48.T2 chat bridge (registers itself); HandlerCallbackQuery: S-48.T4's approval flow.
	if _, err := NewCascadePABridgeChatHandler(ctx, ChatWiringDeps{DataDir: deps.DataDir, Events: deps.Events}, module, stores.Binding, subject, deps.Clock); err != nil {
		return nil, closeStateOnError(state, err)
	}
	module.RegisterHandler(telegram.HandlerCallbackQuery, telegram.NewApprovalHandler(telegram.ApprovalHandlerDeps{
		Subject:   subject,
		Binding:   stores.Binding,
		Callbacks: stores.Callback,
		Approvals: newTelegramApprovalService(client.UnixDialer, telegramApprovalClientTimeout, runtime.NewDefaultPathProvider),
		Events:    journal,
		Clock:     deps.Clock,
		Answer:    module.Answer,
	}))
	routes := []string{telegram.HandlerText, telegram.HandlerCallbackQuery}
	return &BridgeRuntime{
		Subject:        subject,
		Start:          module.Start,
		Stop:           closeStateAfterStop(module.Stop, state),
		IssueCode:      bridgeIssuer(stores, deps.Clock, subject),
		sink:           journal,
		scanner:        scanner,
		Routes:         routes,
		ApprovalBridge: approvalLeg,
	}, nil
}

// closeStateOnError and closeStateAfterStop (the durable-state close helpers
// this file's enabledBridge and BridgeRuntime.Stop use) live in the sibling
// file cascadepa_bridge_state_close.go — moved there to keep this file under
// the 300-line cap (P1-E23-W5-S48-T4 producer fix, mechanical split, no
// behaviour change).

// bridgeIssuer builds the pa.pair_code implementation for a CONFIGURED bridge.
//
// An empty subject means "this bridge", which is the normal call. A subject
// that is not this bridge's is REFUSED rather than issued: the code is stored
// as a digest keyed from this bot's token, so a code minted under another
// subject could never be verified by anything — issuing one would look like a
// working command and fail silently ten minutes later, in Telegram.
func bridgeIssuer(stores *cascadepa.Stores, clock cascadepa.PairClock,
	subject string) func(context.Context, string) (BridgePairCode, error) {
	return func(ctx context.Context, want string) (BridgePairCode, error) {
		if want != "" && want != subject {
			return BridgePairCode{}, cascade.Newf(cascade.KindInvalidInput,
				"cascade pa pair: this daemon runs no bridge for subject %q; its configured bridge is %q",
				want, subject)
		}
		code, err := stores.Pairing.IssueCode(ctx, rand.Reader, subject)
		if err != nil {
			return BridgePairCode{}, err
		}
		return BridgePairCode{
			Code:      code,
			Subject:   subject,
			ExpiresAt: clock.Now().Add(cascadepa.PairCodeTTL),
		}, nil
	}
}

// runtime.SystemClock satisfies the plugin-facing clock the deps above take, so
// the production call site can pass the host clock unchanged.
var _ cascadepa.PairClock = runtime.SystemClock{}
