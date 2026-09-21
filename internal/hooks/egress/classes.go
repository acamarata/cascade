package egress

// The egress classes registered at package init. Each names the ticket
// that owns the outbound path, so a refusal points at the code that
// declared the policy rather than at this file.
const (
	// EgressClassMCP is the daemon's tool-protocol response path to a
	// harness connection.
	EgressClassMCP EgressClass = "mcp.response"
	// EgressClassHook is the hook engine's outbound action crossing.
	EgressClassHook EgressClass = "hook.response"
	// EgressClassTelemetry is the deferred telemetry path. It is
	// registered DISABLED and therefore refuses every call.
	//
	// CASCADE-ALLOW: P1-E08-W2-S16-T1 Telemetry egress is explicitly
	// deferred to a later phase; no document, help text or command claims
	// that telemetry egress is active. It is registered so that a caller
	// reaching for it gets a named refusal, not an unknown-class error
	// that reads like a typo.
	EgressClassTelemetry EgressClass = "telemetry"
	// EgressClassOAuth is the token-endpoint call the vault's OAuth
	// broker makes.
	EgressClassOAuth EgressClass = "oauth"
	// EgressClassProviderIntake is the provider-intake fetch. The health
	// and recovery probes reuse this class and never register their own.
	EgressClassProviderIntake EgressClass = "provider-intake"
	// EgressClassSpikeMeasurement is a measurement spike's fetch. It is
	// deleted with the spike and is never a shipped lane.
	EgressClassSpikeMeasurement EgressClass = "spike-measurement"
	// EgressClassBackupTarget is a remote backup destination. It is the
	// one class here that admits restricted content, because a backup of
	// restricted material is the point of a backup.
	EgressClassBackupTarget EgressClass = "backup-target"
	// EgressClassPluginRemote is the remote plugin runtime. It is
	// registered DISABLED and stays disabled until the operator sets the
	// remote-runtime config key, so the default posture is a closed door.
	EgressClassPluginRemote EgressClass = "plugin-remote"
	// EgressClassRegistryFetch is the plugin registry fetch.
	EgressClassRegistryFetch EgressClass = "registry-fetch"
	// EgressClassCIPoll is the GitHub Actions REST polling client's
	// outbound fetch (P1-E25-W5-S51-T2, 06 §5.17). The ticket's own text
	// names this class's home as "internal/secrets/classes.go" -- that
	// file does not exist in this tree; the real registry, and every
	// other landed egress class, lives here. Registered here rather than
	// there.
	EgressClassCIPoll EgressClass = "ci-poll"
	// EgressClassSync is the server<->local<->node sync engine's
	// outbound leg (§D-30, P1-E17-W4-S38-T1, 06 §5.17): every chunk,
	// cursor frame, and acknowledgement the sync engine writes to a peer
	// transits this class. Registered here per this ticket's own
	// registrant convention (internal/sync/egress.go calls RegisterClass
	// at its own init over this constant); AllowRestricted is NOT set —
	// the pre-serialization filter (internal/sync/filter.go) already
	// refuses local-only/restricted records before a byte reaches this
	// class, so this class itself stays as strict as every other default.
	EgressClassSync EgressClass = "sync"
	// EgressClassBridge is the personal-assistant bridge's outbound leg
	// (P1-E23-W5-S48-T1, R-21.227): every Telegram getUpdates poll and
	// sendMessage call this ticket's plugins/cascade-pa/telegram module
	// makes transits this class, resolved through the C/S-05.T7 host
	// services handle before the first network call. AllowRestricted is
	// NOT set and AllowedTiers admits only {internal, public}: a
	// local-only or restricted thread can never reach the bridge (that
	// refusal is W/S-48.T2's chat-parity gate, enforced upstream of this
	// class). This entry carries no net scopes of its own — the exact
	// host this module dials (api.telegram.org) is enforced at the HTTP
	// call site (plugins/cascade-pa/telegram/apicall.go's httpDoer, over
	// transport.go's poster), not
	// by this registry, matching EgressClassSync/EgressClassNodeDispatch's
	// identical convention below.
	EgressClassBridge EgressClass = "bridge"
	// EgressClassNodeDispatch is the remote node-dispatch leg (§D-30,
	// P1-E17-W4-S37-T2, 06 §5.17): every byte the controller ships to an
	// enrolled node transits it — git refs on the push/fetch legs, RPC
	// frames over the ssh tunnel, journal-stream records coming back, and
	// per-dispatch token handoffs. Registered by its owner at
	// internal/nodes/egress.go's init, per the same registrant convention
	// EgressClassSync follows. AllowRestricted is NOT set: restricted work
	// reaches a node only when placement already proved the node's trust
	// tier, and this class stays as strict as every other default so a
	// caller cannot widen admission by writing straight to it.
	EgressClassNodeDispatch EgressClass = "node-dispatch"
	// EgressClassNselfBackend is the cascade-nself plugin's outbound leg
	// (P1-E25-W5-S52-T2, 06 §5.17): every tool response that plugin
	// emits transits this class. Today that is its project-detection
	// report; a future ticket's real backend leg (if the counterpart CLI
	// ever grows the handshake verb its contract assumed) inherits the
	// same class rather than registering a second one.
	// plugins/nself cannot import this package at all (Art.10.2,
	// plugins-providers-boundary depguard) so it carries its own local,
	// string-identical mirror (plugins/nself/doctor.go's EgressClass/
	// SensitivityTier) and writes through an injected EgressInterceptor
	// seam — the same shape internal/plugins/process/types.go documents
	// for the identical depguard reason. The REAL engine is bound to that
	// seam by internal/plugins/nself_wiring.go; with nothing bound the
	// plugin refuses to emit rather than passing bytes through unfiltered,
	// so this config is enforced rather than decorative.
	// AllowRestricted is NOT set: no restricted-tier value has a path into
	// that plugin's detection flow to begin with.
	EgressClassNselfBackend EgressClass = "nself-backend"
	// EgressClassWikiGitPush is the cascade-github plugin's wiki git push
	// (P1-E25-W5-S51-T6, 06 §5.17): `cascade github wiki sync`'s clone and
	// push of a repository's github.com/{owner}/{repo}.wiki.git endpoint.
	// It is registered here for the central inventory this file's own doc
	// comment describes, and the cascade-github manifest's declared net
	// scope is extended to github.com to match.
	//
	// This entry is DOCUMENTATION-AND-AUDIT, not a live enforcement point
	// this class's traffic actually transits: plugins/github/wiki (like
	// plugins/github's existing api.github.com calls, P1-E25-W5-S51-T1's
	// "declared, accepted-risk design, trusted tier") is a PROCESS-tier
	// plugin — a separate OS process from the daemon that owns this
	// registry — so plugins/** cannot import this package (Art.10.2) and
	// cannot call Engine.Intercept across the process boundary the way a
	// same-process builtin plugin does (contrast
	// internal/plugins/nself_wiring.go's live EgressInterceptor binding,
	// which only works because cascade-nself links into the same binary).
	// The trusted-tier consent the operator grants when enabling
	// cascade-github (the manifest's `requires` + permissions display) is
	// this class's real gate — exactly as it already is for this same
	// plugin's own api.github.com calls (plugins/github/tools' HTTPDoer),
	// which likewise register no egress class of their own and rely
	// entirely on that same consent boundary. This is not an invented
	// exception: 06-FORGE-SPEC.md's binding rule 21 (O/S-31.T3, "process-tier
	// plugin custody") states it exactly — "their DIRECT network egress is
	// declared accepted-risk in the trusted-tier consent warning, with
	// declared net scopes recorded and audited" — which this entry plus the
	// manifest's extended net scope is. The confirming review (P1-E25-W5-S51-T6
	// PRE-RULING 2) confirmed rule 21 covers this axis; it does NOT cover
	// the separate local-file/argv-disclosure risk runner.go's gitAuthEnv
	// addresses (D3).
	EgressClassWikiGitPush EgressClass = "wiki-git-push"
	// EgressClassRecallWhat is the recall.what RPC response path
	// (P1-E22-W5-S47-T1, H/S-16.T1): every conversation snippet, memory
	// body, file path, and domain error string the fused cross-domain
	// recall answer carries transits this class immediately before it is
	// written to the wire, so the substitution pass runs on the actual
	// bytes leaving the process rather than on a hand-rolled detector
	// call the boundary could forget. It is its own class, distinct from
	// EgressClassMCP (the tool-protocol response path): recall.what is a
	// plain internal/rpc method, not an MCP tool response, and this
	// package's own rule is that two subsystems answering to different
	// owners never share a class. AllowedTiers admits only
	// {internal, public}, the same narrowing EgressClassBridge uses: a
	// thread or record classified local-only is refused here rather than
	// silently substituted, which is the mechanism that replaces the
	// caller-declared-tier trust problem this ticket's adversarial review
	// found (a caller cannot assert its way past a local-only exclusion
	// it does not control).
	//
	// SCOPE NOTE: this class did not exist before this ticket and this
	// file is outside T-1's files_scope; adding it here is a recorded,
	// minimal scope deviation — the alternative (reusing EgressClassMCP)
	// would have been exactly the class-sharing mistake this package's
	// header warns against, and skipping real egress enforcement is what
	// the adversarial review rejected the previous draft for.
	EgressClassRecallWhat EgressClass = "recall-what"
)

// defaultClasses is the registration table. It is a slice of pairs rather
// than a map so registration order is fixed and a reader sees the whole
// policy in one place, in one order.
var defaultClasses = []struct {
	Class  EgressClass
	Config InterceptConfig
}{
	{EgressClassMCP, InterceptConfig{Enabled: true, Owner: "D/S-06.T6"}},
	{EgressClassHook, InterceptConfig{Enabled: true, Owner: "C/S-05.T1"}},
	{EgressClassTelemetry, InterceptConfig{Enabled: false, Owner: "P1-E08-W2-S16-T1"}},
	{EgressClassOAuth, InterceptConfig{Enabled: true, Owner: "H/S-15.T2"}},
	{EgressClassProviderIntake, InterceptConfig{Enabled: true, Owner: "J/S-20.T1"}},
	{EgressClassSpikeMeasurement, InterceptConfig{Enabled: true, Owner: "F/S-12.T5"}},
	{EgressClassBackupTarget, InterceptConfig{Enabled: true, AllowRestricted: true, Owner: "S/S-41.T3"}},
	{EgressClassPluginRemote, InterceptConfig{Enabled: false, Owner: "O/S-33.T4"}},
	{EgressClassRegistryFetch, InterceptConfig{Enabled: true, Owner: "X/S-50.T1"}},
	{EgressClassCIPoll, InterceptConfig{Enabled: true, Owner: "P1-E25-W5-S51-T2"}},
	{EgressClassBridge, InterceptConfig{
		Enabled: true, AllowRestricted: false,
		AllowedTiers: []SensitivityTier{TierInternal, TierPublic},
		Owner:        "P1-E23-W5-S48-T1",
	}},
	{EgressClassNselfBackend, InterceptConfig{
		Enabled: true, AllowRestricted: false,
		AllowedTiers: []SensitivityTier{TierInternal},
		Owner:        "P1-E25-W5-S52-T2",
	}},
	{EgressClassWikiGitPush, InterceptConfig{Enabled: true, Owner: "P1-E25-W5-S51-T6"}},
	{EgressClassRecallWhat, InterceptConfig{
		Enabled: true, AllowRestricted: false,
		AllowedTiers: []SensitivityTier{TierInternal, TierPublic},
		Owner:        "P1-E22-W5-S47-T1",
	}},
}

// defaultRegistry holds the classes this build ships with. It is package
// state because registration happens at init, which is what makes a class
// available before any composition root has run.
var defaultRegistry = newDefaultRegistry()

// newDefaultRegistry builds and populates the default registry. A failed
// registration panics: it means this build has two policies for one
// outbound path, and choosing one at random is not an option a firewall
// gets to take.
func newDefaultRegistry() *Registry {
	r := NewRegistry()
	for _, entry := range defaultClasses {
		r.MustRegister(entry.Class, entry.Config)
	}
	return r
}

// DefaultRegistry returns the registry carrying the classes this build
// registers at init. A registrant landing a new outbound path registers
// on this registry; a test that needs isolation builds its own with
// NewRegistry.
func DefaultRegistry() *Registry { return defaultRegistry }
