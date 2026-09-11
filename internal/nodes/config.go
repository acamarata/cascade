// Purpose: the [nodes] config section (enroll defaults), per 08-INIT-
//
//	CONFIG-SPEC.md §3.
//
// Inputs: the decoded config.toml tree's "nodes" table.
// Outputs: a Section value with this ticket's defaults applied.
// Constraints: the contract text asks this ticket to "register the
//
//	[nodes] config section ... through the C/S-04.T1 config frame (hot
//	reload, tightening-only per C/S-05.T8 §D-26)." The real C/S-04.T1
//	frame (internal/runtime/config.go, read-only reference for this
//	ticket, NOT in files_scope and actively owned by a concurrent agent)
//	has no section-registration seam: [runtime], [elevation], [logging]
//	and [retrieval] are each a typed struct field on *runtime.Config,
//	decoded by parseConfigSections INSIDE internal/runtime itself — there
//	is no exported "RegisterSection" hook any outside package can call.
//	See the ticket journal's CONTRADICTIONS section for the full quote.
//	This file therefore does the parsing half only (parseSection, over
//	the raw map[string]interface{} the file's Extra field already
//	preserves verbatim for every section this ticket does not own) and
//	documents the wiring gap rather than editing a file outside this
//	ticket's scope. A follow-up ticket that DOES own config.go completes
//	the registration by adding a Nodes field and calling parseSection.
//
// SPORT: internal/nodes Section/ADDED (P1-E17-W4-S36-T1).

package nodes

import "github.com/acamarata/cascade/pkg/cascade"

// Section is the [nodes] config table: enrollment defaults applied when a
// `node enroll` invocation omits the corresponding flag.
type Section struct {
	// DefaultTrustTier is the tier `node enroll` assumes when
	// --trust-tier is omitted. Per R-21.220 the CLI's --trust-tier flag
	// is REQUIRED (S-36.T4 mounts it as required), so this default is
	// consulted only by callers that explicitly opt in to it; it is never
	// used to silently satisfy the "no trust_tier is a typed error, never
	// defaulted" rule at the enroll-payload validation layer (ValidateTier
	// in trust.go never reads this section).
	DefaultTrustTier string `toml:"default_trust_tier"`
	// KnownHostsPath overrides the known_hosts store location
	// (<data_dir>/nodes/known_hosts.json when empty).
	KnownHostsPath string `toml:"known_hosts_path"`
	// ScanLAN gates LAN discovery advertising/browsing (R-21.198,
	// P1-E36-W7-S72-T1). Default false: discovery is opt-in.
	ScanLAN bool `toml:"scan_lan"`
	// DiscoveryNetworks allowlists the interface/SSID names discovery may
	// run on when ScanLAN is true. Default empty: an empty allowlist
	// means discovery runs on NO network — fail-closed (R-21.198). A
	// network not in this list (e.g. one never paired on before) is
	// never scanned, even with ScanLAN=true.
	DiscoveryNetworks []string `toml:"discovery_networks"`
}

// parseSection fail-closed-parses raw (the "nodes" sub-tree of a decoded
// config.toml document, or nil if the section is absent) into a Section.
// An absent section returns the zero Section and a nil error (no [nodes]
// table is valid — every field has a safe empty default). A present but
// malformed DefaultTrustTier value (set and non-empty but not one of the
// three tier strings) is refused rather than silently accepted, per this
// package's fail-closed convention — a config-time typo must not surface
// only when someone finally omits --trust-tier.
func parseSection(raw map[string]interface{}) (Section, error) {
	if raw == nil {
		return Section{}, nil
	}
	var sec Section
	if v, ok := raw["default_trust_tier"]; ok {
		s, ok := v.(string)
		if !ok {
			return Section{}, cascade.New(cascade.KindInvalidInput, "nodes: [nodes].default_trust_tier must be a string")
		}
		if s != "" {
			if _, err := ValidateTier(s, true); err != nil {
				return Section{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: [nodes].default_trust_tier")
			}
		}
		sec.DefaultTrustTier = s
	}
	if v, ok := raw["known_hosts_path"]; ok {
		s, ok := v.(string)
		if !ok {
			return Section{}, cascade.New(cascade.KindInvalidInput, "nodes: [nodes].known_hosts_path must be a string")
		}
		sec.KnownHostsPath = s
	}
	if err := parseDiscoverySection(raw, &sec); err != nil {
		return Section{}, err
	}
	return sec, nil
}

// parseDiscoverySection parses scan_lan/discovery_networks into sec, split
// out of parseSection to keep it under the 50-line cap.
func parseDiscoverySection(raw map[string]interface{}, sec *Section) error {
	if v, ok := raw["scan_lan"]; ok {
		b, ok := v.(bool)
		if !ok {
			return cascade.New(cascade.KindInvalidInput, "nodes: [nodes].scan_lan must be a bool")
		}
		sec.ScanLAN = b
	}
	if v, ok := raw["discovery_networks"]; ok {
		list, ok := v.([]interface{})
		if !ok {
			return cascade.New(cascade.KindInvalidInput, "nodes: [nodes].discovery_networks must be a list of strings")
		}
		nets := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return cascade.New(cascade.KindInvalidInput, "nodes: [nodes].discovery_networks entries must be strings")
			}
			nets = append(nets, s)
		}
		sec.DiscoveryNetworks = nets
	}
	return nil
}
