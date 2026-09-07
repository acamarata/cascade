package hookpacks

// Purpose (this file): R-16.48's HookRegistry — the internal dispatch
//
//	table other subsystems register hook packs into, and the single
//	Render call that unions every registered pack's descriptors into one
//	installable hook configuration for a given daemon socket path.
//
// Inputs: named HookPacks, registered via RegisterPack.
// Outputs: Packs() returns the registered packs in registration order;
//
//	Render(socket) returns the marshaled union configuration, grouped by
//	event type, or nil on a rendering failure (fail-closed: never a
//	partial or panic'd result — see Render's own comment).
//
// Constraints: fleet.sessions.hook_event (handler.go) is the SOLE
//
//	daemon-side JSON-RPC entry point every pack's rendered commands call
//	back into; HookRegistry is not a second wire protocol, it only
//	assembles the installable command list. DefaultRegistry is a
//	package-level singleton other packages' init() functions register
//	into (e.g. this ticket's own "sessions" pack, below) — concurrent
//	RegisterPack/Packs/Render calls are safe (RWMutex-guarded), matching
//	internal/rpc.Registry's own concurrency contract.
//
// SPORT: fleet/hookpacks.HookRegistry/ADDED (P1-E12-W3-S24-T4).
import (
	"encoding/json"
	"sync"
)

// HookRegistry is the named-pack dispatch table other subsystems
// register additional hook packs into. P/S-34.T1 (the install wizard)
// calls Render to install the rendered union into a harness's hook
// configuration; AF/S-66.T1 registers a pack named "completion-gate" and
// P/S-34.T4 registers one named "hydration", both via RegisterPack — this
// ticket owns the type and registers only its own "sessions" pack. The
// zero value is not ready to use; construct with NewHookRegistry.
type HookRegistry struct {
	mu    sync.RWMutex
	order []string
	packs map[string]HookPack
}

// NewHookRegistry returns an empty, ready-to-use HookRegistry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{packs: make(map[string]HookPack)}
}

// RegisterPack binds name to p. Registering the same name twice
// overwrites the pack's contents but keeps its original position in
// registration order — mirroring internal/rpc.Registry.Register's own
// documented "re-registration is not an error" contract, for the same
// hot-reloadable-plugin reason.
func (r *HookRegistry) RegisterPack(name string, p HookPack) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.packs[name]; !exists {
		r.order = append(r.order, name)
	}
	r.packs[name] = p
}

// Packs returns every registered HookPack in registration order. The
// returned slice is a fresh copy; mutating it never affects the
// registry.
func (r *HookRegistry) Packs() []HookPack {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HookPack, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.packs[name])
	}
	return out
}

// hookConfig is the top-level installable hook configuration shape:
// event type name -> the config entries for that event, across every
// registered pack.
type hookConfig struct {
	Hooks map[HookEventType][]json.RawMessage `json:"hooks"`
}

// Render renders every registered pack's descriptors against socket and
// returns the marshaled union hook configuration, grouped by event type
// (delegating each pack's own rendering to HookPack.Render). A socket
// path that fails to render (currently: only an empty socket,
// ErrEmptySocketPath) makes Render return nil rather than a partial or
// panicking result — fail-closed, since a partially-rendered
// configuration missing some packs' hooks would silently under-install.
// Render never blocks: every step is pure string/JSON work, no I/O.
func (r *HookRegistry) Render(socket string) []byte {
	r.mu.RLock()
	packs := make([]HookPack, 0, len(r.order))
	for _, name := range r.order {
		packs = append(packs, r.packs[name])
	}
	r.mu.RUnlock()

	cfg := hookConfig{Hooks: make(map[HookEventType][]json.RawMessage)}
	for _, p := range packs {
		rendered, err := p.Render(socket)
		if err != nil {
			return nil
		}
		for i, d := range p.Descriptors {
			cfg.Hooks[d.EventType] = append(cfg.Hooks[d.EventType], rendered[i])
		}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil
	}
	return out
}

// DefaultRegistry is the package-level HookRegistry every subsystem's
// init() registers its hook pack into (R-16.48). P/S-34.T1's install
// wizard calls DefaultRegistry.Render to build the union it installs.
var DefaultRegistry = NewHookRegistry()

func init() {
	DefaultRegistry.RegisterPack("sessions", SessionsPack())
}
