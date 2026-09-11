package plugin

// Purpose: SDK descriptor/helper types the author kit adds on top of the
//
//	C/S-05.T6 manifest v2 baseline (manifest.go) — types that baseline does
//	not already declare. AgentProviderMethod names the five verbs a
//	manifest's provides entry can bind to on the guest side (guestshim.go);
//	it is the typed vocabulary R-14.50's "provides -> AgentProvider method
//	mapping" resolves through.
//
// Inputs: none (pure type declarations).
// Outputs: AgentProviderMethod and its closed set of members, consumed by
//
//	guestshim.go's GuestDispatcher.
//
// Constraints: no duplication of manifest.go's Manifest, IntentSpec,
//
//	ToolSpec, DomainSpec, CommandSpec, or ValidationError; pkg/plugin never
//	imports internal/ (Art.10.2); no bare fmt.Errorf/errors.New (boundary
//	lint — vacuous here, this file mints no errors).
//
// SPORT: pkg/plugin author-kit-types (ADD) — P1-E15-W4-S33-T1.

// AgentProviderMethod names one of the five verbs an AgentProvider
// implementation (pkg/provider.AgentProvider) exposes. A manifest's
// provides.tools or provides.intents entry binds its Name to one of these
// methods; guestshim.go's GuestDispatcher.Register takes an
// AgentProviderMethod rather than a bare string so a guest binary cannot
// register a typo'd or invented verb.
type AgentProviderMethod string

// The closed set of AgentProviderMethod values, mirroring the five verbs
// pkg/provider.AgentProvider (and pkg/provider.ModelProvider, the same
// five-verb contract) declares. There is no sixth per 06-FORGE-SPEC.md §5
// rule 11.
const (
	// MethodChat binds to AgentProvider.Chat.
	MethodChat AgentProviderMethod = "chat"
	// MethodEmbed binds to AgentProvider.Embed.
	MethodEmbed AgentProviderMethod = "embed"
	// MethodCount binds to AgentProvider.Count.
	MethodCount AgentProviderMethod = "count"
	// MethodStream binds to AgentProvider.Stream.
	MethodStream AgentProviderMethod = "stream"
	// MethodCapabilities binds to AgentProvider.Capabilities.
	MethodCapabilities AgentProviderMethod = "capabilities"
)

// Valid reports whether m is one of the five declared AgentProviderMethod
// members. The zero value (empty string) is not valid.
func (m AgentProviderMethod) Valid() bool {
	switch m {
	case MethodChat, MethodEmbed, MethodCount, MethodStream, MethodCapabilities:
		return true
	default:
		return false
	}
}

// String returns m's wire value. Defined explicitly (rather than relying on
// the underlying string type) so AgentProviderMethod satisfies fmt.Stringer
// and prints its wire form, not a Go-syntax quoted string, in error
// messages built with %s.
func (m AgentProviderMethod) String() string {
	return string(m)
}
