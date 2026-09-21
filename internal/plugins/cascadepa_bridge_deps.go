package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"

	"github.com/pelletier/go-toml/v2"
)

// Purpose (this file): the host seams cascadepa_bridge_wiring.go needs — the
//   module flag, the token read, the egress gate, the elevation policy and the
//   paired-device registrar — split out for Art.10.3's 300-line cap and by
//   subject: that file is the composition, this one is the adapters, and
//   cascadepa_bridge_state.go is the durable store.
//
// Inputs: the resolved data directory.
// Outputs: the module flag, the bot token, the egress gate and the
//   paired-device registrar.
//
// Constraints, each one a decision rather than a default:
//   - THE EGRESS GATE IS THE REAL ENGINE. Guard is a direct call to
//     egress.Engine.InterceptClass on EgressClassBridge, exactly as
//     internal/ci/poll.go and internal/mcp/firewall.go reach their own
//     classes, so AllowRestricted:false and AllowedTiers{internal,public} are
//     enforced on the Telegram send path and the substitution pass rewrites
//     what is actually posted.
//   - ELEVATION CLASSIFICATION IS THE CANONICAL TABLE. bridgeElevationPolicy
//     defers to internal/rpc.IsElevated — the one §5.14 table, the same one
//     the JSON-RPC middleware consults — rather than a second list inside the
//     plugin. Params are nil on this path, and every conditional rule in that
//     table FAILS CLOSED on unreadable params, so a conditionally-elevated
//     verb arriving over the bridge is refused.
//   - THE PAIRED-DEVICE RECORD IS A REAL Q/S-36.T1 DeviceRecord, written
//     through nodes.RecordStore at trust tier paired-device — but into the
//     bridge's OWN backend directory, not the node fleet's devices.json. Two
//     reasons: a bridge binding is not a node enrollment (this ticket's
//     contract says so in as many words), and a bridge record carries NO
//     public key, so putting it in the fleet's registry would hand every
//     fleet consumer a keyless row it has no rule for. Keylessness is the
//     point: nothing can sign a dispatch frame as this device, so the record
//     satisfies no node-dispatch gate by construction rather than by policy.
//
// SPORT: internal/plugins:cascadepa-bridge-deps (ADD) — P1-E23-W5-S48-T1.

// telegramModuleConfig is the module gate read from the cascade-pa module
// manifest.
type telegramModuleConfig struct {
	Enabled  bool
	VaultKey string
}

// moduleManifest is the on-disk shape readTelegramModuleConfig parses.
type moduleManifest struct {
	Modules struct {
		Telegram struct {
			Enabled          bool   `toml:"enabled"`
			BotTokenVaultKey string `toml:"bot_token_vault_key"`
		} `toml:"telegram"`
	} `toml:"modules"`
}

// bridgeManifestPath is where an operator enables the module: a manifest
// under the data directory, never an environment variable and never a
// config.toml key (this ticket's own gate, and R-21.227's "opt-in MODULE").
func bridgeManifestPath(dataDir string) string {
	return filepath.Join(dataDir, "plugins", "cascade-pa", "manifest.toml")
}

// readTelegramModuleConfig reads the module gate. An ABSENT manifest is the
// default-off answer, not an error: a host that never opted in is the normal
// case, and reporting it as a failure would make every `cascade pa pair` run
// look broken.
func readTelegramModuleConfig(dataDir string) (telegramModuleConfig, error) {
	raw, err := os.ReadFile(bridgeManifestPath(dataDir)) //nolint:gosec // a path this function builds itself
	if errors.Is(err, os.ErrNotExist) {
		return telegramModuleConfig{Enabled: false, VaultKey: bridgeVaultKey}, nil
	}
	if err != nil {
		return telegramModuleConfig{}, cascade.Wrap(cascade.KindUnavailable, err,
			"cascade-pa bridge: read the cascade-pa module manifest")
	}
	var m moduleManifest
	if err := toml.Unmarshal(raw, &m); err != nil {
		return telegramModuleConfig{}, cascade.Wrap(cascade.KindInvalidInput, err,
			"cascade-pa bridge: the cascade-pa module manifest is not valid TOML")
	}
	key := strings.TrimSpace(m.Modules.Telegram.BotTokenVaultKey)
	if key == "" {
		key = bridgeVaultKey
	}
	return telegramModuleConfig{Enabled: m.Modules.Telegram.Enabled, VaultKey: key}, nil
}

// openBridgeBroker selects custody and builds the broker the standing-grant
// token read needs. The nil elevation gate is deliberate: Broker.Get still
// refuses without one, so this path gains no elevated read — only GetGranted
// succeeds, and only for a key a human has granted.
func openBridgeBroker(cfg secrets.Config) (*secrets.Broker, error) {
	custody, err := secrets.SelectCustody(cfg)
	if err != nil {
		return nil, err
	}
	return secrets.NewBroker(custody, nil)
}

// firstErr returns the first non-nil error, or nil. It exists so a chain of
// constructors that each fail only on a nil argument is checked once rather
// than guarded four times, with three of those guards unreachable by
// construction and therefore unprovable.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// resolveBridgeToken reads the bot token from real custody under a standing
// grant — the headless daemon's only sanctioned read path (R-14.243). No
// environment variable and no config value can supply it, and Broker.Get
// (the elevated verb) is never called, so this path gains no elevated read.
func resolveBridgeToken(ctx context.Context, cfg secrets.Config, key string) (string, error) {
	if key == "" {
		key = bridgeVaultKey
	}
	// COLLECT, THEN CHECK. Each constructor below fails only on a nil or empty
	// argument, and each TOLERATES a nil input from the step before it
	// (NewGrants refuses a nil store, GetGranted refuses a nil register), so
	// one check reports the first real failure instead of three guards that
	// can only ever fire on the first one.
	broker, berr := openBridgeBroker(cfg)
	store, serr := secrets.NewFileGrantStore(cfg.Dir)
	grants, gerr := secrets.NewGrants(store, runtime.NewSystemClock())
	if err := firstErr(berr, serr, gerr); err != nil {
		return "", err
	}
	value, err := broker.GetGranted(ctx, key, grants)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(value))
	if token == "" {
		return "", cascade.Newf(cascade.KindUnavailable,
			"cascade-pa bridge: vault key %s is empty; the telegram module cannot start", key)
	}
	return token, nil
}

// NewBridgeEgressGate adapts an egress engine onto the bridge's plugin-facing
// gate. Exported so the bridge-class red-team suite can drive the REAL
// Telegram send path through the real class rather than calling InterceptClass
// beside it and ratifying a path production never takes.
func NewBridgeEgressGate(engine *egress.Engine) telegram.EgressGate {
	return bridgeEgressGate{engine: engine}
}

// bridgeEgressGate is the production EgressGate.
type bridgeEgressGate struct {
	engine *egress.Engine
}

// Guard runs the firewall over content at tier and returns the bytes the
// caller may write.
func (g bridgeEgressGate) Guard(
	ctx context.Context, tier cascadepa.SensitivityTier, content []byte,
) ([]byte, error) {
	if g.engine == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"cascade-pa bridge: no egress engine is configured; refusing to write")
	}
	return g.engine.InterceptClass(ctx, egress.EgressClassBridge, egress.SensitivityTier(tier), content)
}

// newBridgeEgressGateFor adapts this host's bridge firewall onto the
// plugin-facing gate. The engine itself is built by internal/bridge — see that
// package's firewall.go for why the vault construction lives there and not
// here (the non-elevated read is granted to one small package, not to the whole
// plugin-wiring tree).
func newBridgeEgressGateFor(cfg secrets.Config) (telegram.EgressGate, error) {
	engine, err := bridge.NewFirewall(cfg)
	if err != nil {
		return nil, err
	}
	return NewBridgeEgressGate(engine), nil
}

// bridgeElevationPolicy answers the bridge's elevated-verb question from the
// canonical §5.14 table.
type bridgeElevationPolicy struct{}

// IsElevatedVerb defers to internal/rpc's table. Params are nil: a bridge
// message carries no RPC params, and every conditional rule in that table
// treats unreadable params as "elevate", which is the answer this path wants.
func (bridgeElevationPolicy) IsElevatedVerb(verb string) bool {
	return rpc.IsElevated(verb, nil)
}

// bridgeDeviceRegistrar writes the paired-device record.
type bridgeDeviceRegistrar struct {
	store *nodes.RecordStore
}

// newBridgeDeviceRegistrar builds the registrar over the bridge's own record
// backend (dataDir/bridge/nodes/devices.json) — see this file's doc comment
// for why it is not the node fleet's registry.
func newBridgeDeviceRegistrar(dataDir string) cascadepa.DeviceRegistrar {
	backend := nodes.NewFileRecordBackend(filepath.Join(dataDir, "bridge"))
	return bridgeDeviceRegistrar{store: nodes.NewRecordStore(backend, runtime.NewSystemClock())}
}

// RegisterPairedDevice records subject/senderID at trust tier paired-device
// and returns the record's node id. Re-pairing the same sender is idempotent:
// the record already exists, which is a success for this caller rather than
// the conflict a re-enrollment of a live NODE identity would be.
//
// The Identity carries a node id and NO public key, which is deliberate: a
// Telegram sender has no keypair, and inventing one would make the record look
// able to sign frames it can never sign. nodes.ParsePublicKey refuses an empty
// key, so every signature-verifying path refuses this device by construction —
// the "grants no node-dispatch capability" property is structural, not a rule
// somebody has to remember.
func (r bridgeDeviceRegistrar) RegisterPairedDevice(
	_ context.Context, subject, senderID string, _ time.Time,
) (string, error) {
	nodeID := bridgeNodeID(subject, senderID)
	rec, err := r.store.Enroll(nodes.Identity{NodeID: nodeID}, nodes.TierPairedDevice)
	if err != nil {
		if cascade.HasKind(err, cascade.KindConflict) {
			return nodeID, nil
		}
		return "", err
	}
	return rec.NodeID, nil
}

// bridgeNodeID derives the paired device's stable node id from the subject and
// the sender. Deterministic, so re-pairing the same sender addresses the same
// record instead of accumulating one per attempt. The NUL separator keeps
// ("a","bc") and ("ab","c") from colliding.
func bridgeNodeID(subject, senderID string) string {
	sum := sha256.Sum256([]byte(subject + "\x00" + senderID))
	return "bridge-" + hex.EncodeToString(sum[:])[:16]
}
