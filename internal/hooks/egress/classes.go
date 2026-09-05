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
