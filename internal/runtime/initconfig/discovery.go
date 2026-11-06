package initconfig

// Purpose: the [roots], [nodes] and [policy] tables of cascade.init/v1
//   (R-16.34) and the layered resolver that turns a file plus the env and
//   flag overlays into one Discovery.
// Inputs: a parsed *InitConfig (nil allowed) and two overlays whose nil
//   fields mean "this layer is silent".
// Outputs: a validated Discovery, or Discovery{} and a KindInvalidInput
//   refusal of the form "cascade init: <key>: <reason>".
// Constraints: this file reads no environment; mapping CASCADE_INIT_*
//   names onto an overlay belongs to the env layer. Slices are copied,
//   never aliased, so a caller mutating its overlay cannot reach into a
//   resolved Discovery.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultRootsMaxFiles is the file-count ceiling a root gets when no layer
// names one.
const DefaultRootsMaxFiles = 200000

// The policy presets a [policy] table may name. An empty preset is unset:
// the wizard's step 8c decides.
const (
	PresetBalanced = "balanced"
	PresetStrict   = "strict"
	PresetCustom   = "custom"
)

// maxHostLen is the longest DNS name a [nodes] pair entry may carry.
const maxHostLen = 253

// hostPattern is the shape of a pair entry: letters, digits, dots and
// hyphens, starting and ending on a letter or digit.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// RootsConfig is the [roots] table: which trees Cascade indexes.
type RootsConfig struct {
	// Paths lists the root directories, as written (a leading ~ is kept).
	Paths []string `toml:"paths"`
	// Maintain keeps the roots indexed after init.
	Maintain bool `toml:"maintain"`
	// MaxFiles caps the files indexed per root; 0 means unset.
	MaxFiles int `toml:"max_files"`
	// AcceptDiscovered adopts roots found by discovery without asking.
	AcceptDiscovered bool `toml:"accept_discovered"`
}

// NodesConfig is the [nodes] table: which machines join the fleet.
type NodesConfig struct {
	// ScanLAN looks for nodes on the local network.
	ScanLAN bool `toml:"scan_lan"`
	// Pair names hosts to pair with at init.
	Pair []string `toml:"pair"`
	// Travel marks this machine as one that leaves the LAN.
	Travel bool `toml:"travel"`
}

// PolicyConfig is the [policy] table.
type PolicyConfig struct {
	// Preset is balanced, strict, custom, or "" (unset).
	Preset string `toml:"preset"`
}

// Discovery is the resolved discovery settings after every layer.
type Discovery struct {
	// Roots is the merged [roots] table.
	Roots RootsConfig
	// Nodes is the merged [nodes] table.
	Nodes NodesConfig
	// Policy is the merged [policy] table.
	Policy PolicyConfig
}

// DiscoveryOverlay is one layer above the file (env or flags). A nil field
// means this layer is silent; a non-nil field replaces the lower layer.
type DiscoveryOverlay struct {
	RootsPaths, NodesPair                                           *[]string
	RootsMaintain, RootsAcceptDiscovered, NodesScanLAN, NodesTravel *bool
	RootsMaxFiles                                                   *int
	PolicyPreset                                                    *string
}

// ResolveDiscovery layers defaults < file < env < flags and validates the
// merged result. Any refusal returns Discovery{} with the error.
func ResolveDiscovery(file *InitConfig, env, flags DiscoveryOverlay) (Discovery, error) {
	d := Discovery{Roots: RootsConfig{MaxFiles: DefaultRootsMaxFiles}}
	if file != nil {
		if err := validateDiscovery(file.Roots, file.Nodes, file.Policy); err != nil {
			return Discovery{}, err
		}
		d.applyFile(file)
	}
	d.apply(env)
	d.apply(flags)
	if err := validateDiscovery(d.Roots, d.Nodes, d.Policy); err != nil {
		return Discovery{}, err
	}
	return d, nil
}

// applyFile lays the file's tables over the defaults. MaxFiles 0 in the
// file is unset and keeps the default.
func (d *Discovery) applyFile(file *InitConfig) {
	d.Roots.Paths = copyStrings(file.Roots.Paths)
	d.Roots.Maintain = file.Roots.Maintain
	d.Roots.AcceptDiscovered = file.Roots.AcceptDiscovered
	if file.Roots.MaxFiles != 0 {
		d.Roots.MaxFiles = file.Roots.MaxFiles
	}
	d.Nodes.ScanLAN = file.Nodes.ScanLAN
	d.Nodes.Pair = copyStrings(file.Nodes.Pair)
	d.Nodes.Travel = file.Nodes.Travel
	d.Policy.Preset = file.Policy.Preset
}

// apply lays one overlay over d; nil fields leave d alone.
func (d *Discovery) apply(o DiscoveryOverlay) {
	if o.RootsPaths != nil {
		d.Roots.Paths = copyStrings(*o.RootsPaths)
	}
	if o.NodesPair != nil {
		d.Nodes.Pair = copyStrings(*o.NodesPair)
	}
	setBool(&d.Roots.Maintain, o.RootsMaintain)
	setBool(&d.Roots.AcceptDiscovered, o.RootsAcceptDiscovered)
	setBool(&d.Nodes.ScanLAN, o.NodesScanLAN)
	setBool(&d.Nodes.Travel, o.NodesTravel)
	if o.RootsMaxFiles != nil {
		d.Roots.MaxFiles = *o.RootsMaxFiles
	}
	if o.PolicyPreset != nil {
		d.Policy.Preset = *o.PolicyPreset
	}
}

// setBool copies *src into dst when src is set.
func setBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

// copyStrings returns a fresh copy of s, keeping nil as nil.
func copyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s...)
}

// validateDiscovery checks the three tables, file or merged.
func validateDiscovery(r RootsConfig, n NodesConfig, p PolicyConfig) error {
	switch p.Preset {
	case "", PresetBalanced, PresetStrict, PresetCustom:
	default:
		return discoveryRefusal("policy.preset", fmt.Sprintf(
			"unknown preset %q; use %q, %q or %q", p.Preset, PresetBalanced, PresetStrict, PresetCustom))
	}
	if r.MaxFiles < 0 {
		return discoveryRefusal("roots.max_files", fmt.Sprintf("%d is negative; use 0 for the default or a positive count", r.MaxFiles))
	}
	if err := validatePaths(r.Paths); err != nil {
		return err
	}
	return validatePair(n.Pair)
}

// validatePaths refuses an empty or duplicated root path.
func validatePaths(paths []string) error {
	seen := map[string]bool{}
	for i, raw := range paths {
		key := fmt.Sprintf("roots.paths[%d]", i)
		path := strings.TrimSpace(raw)
		if path == "" {
			return discoveryRefusal(key, "the path is empty")
		}
		if seen[path] {
			return discoveryRefusal(key, fmt.Sprintf("%q is listed twice", path))
		}
		seen[path] = true
	}
	return nil
}

// validatePair refuses an empty, duplicated, overlong or malformed host.
func validatePair(hosts []string) error {
	seen := map[string]bool{}
	for i, host := range hosts {
		key := fmt.Sprintf("nodes.pair[%d]", i)
		switch {
		case host == "":
			return discoveryRefusal(key, "the host is empty")
		case seen[host]:
			return discoveryRefusal(key, fmt.Sprintf("%q is listed twice", host))
		case len(host) > maxHostLen:
			return discoveryRefusal(key, fmt.Sprintf("the host is longer than %d characters", maxHostLen))
		case !hostPattern.MatchString(host):
			return discoveryRefusal(key, fmt.Sprintf("%q is not a host name", host))
		}
		seen[host] = true
	}
	return nil
}

// discoveryRefusal builds the "cascade init: <key>: <reason>" refusal.
func discoveryRefusal(key, reason string) error {
	return cascade.Newf(cascade.KindInvalidInput, "cascade init: %s: %s", key, reason)
}
