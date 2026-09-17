package init

// Purpose: the desired-state passes over the things a reconverge touches
//   besides config keys (P1-E16-W4-S35-T7): providers, plugins and the
//   generated harness instruction files.
// Inputs: what is installed on the machine, and what the setup file asks
//   for.
// Outputs: per-unit plans naming what to add, re-verify, enable or
//   regenerate, and what to leave alone.
// Constraints: none of these three removes anything. A provider the file
//   does not mention, a plugin the operator toggled themselves, and a
//   harness file somebody edited all survive a reconverge untouched --
//   there is deliberately no Remove field for a later caller to forget.
// SPORT: internal/runtime/init reconverge (ADD) -- P1-E16-W4-S35-T7.

// ProviderConvergence is the desired-state pass over providers.
type ProviderConvergence struct {
	// Add names providers not yet in the registry.
	Add []string `json:"add,omitempty"`
	// Reverify names providers already present, which are re-verified
	// and never re-added.
	Reverify []string `json:"reverify,omitempty"`
}

// ConvergeProviders compares the desired provider set against the
// installed one.
//
// It never returns a REMOVAL. `cascade init` is a setup command; a
// provider the operator added by hand, or from a different setup file, is
// not garbage to be collected because this file does not mention it. The
// asymmetry is deliberate, and is why there is no Remove field for a
// later caller to forget to handle.
func ConvergeProviders(installed, desired []string) ProviderConvergence {
	have := map[string]bool{}
	for _, name := range installed {
		have[name] = true
	}
	out := ProviderConvergence{}
	for _, name := range desired {
		if have[name] {
			out.Reverify = append(out.Reverify, name)
			continue
		}
		out.Add = append(out.Add, name)
	}
	return out
}

// PluginConvergence is the desired-state pass over the plugin catalog.
type PluginConvergence struct {
	// Enable names plugins to turn on.
	Enable []string `json:"enable,omitempty"`
	// Disable names plugins to turn off.
	Disable []string `json:"disable,omitempty"`
	// Kept names plugins a setup file asked to disable and this run left
	// alone, because the operator had explicitly turned them on.
	Kept []string `json:"kept,omitempty"`
}

// ConvergePlugins applies enable and disable against what is on.
//
// `disable` applies ONLY to entries the operator never explicitly
// toggled. A plugin somebody turned on by hand is not turned off by a
// setup file that happens to list it: the same rule the config merge
// follows, for the same reason — a setup run that silently reverted a
// deliberate choice is worse than one that reports it.
func ConvergePlugins(enabled, desiredEnable, desiredDisable, explicitlyToggled []string) PluginConvergence {
	on := setOf(enabled)
	explicit := setOf(explicitlyToggled)
	out := PluginConvergence{}
	for _, name := range desiredEnable {
		if !on[name] {
			out.Enable = append(out.Enable, name)
		}
	}
	for _, name := range desiredDisable {
		if !on[name] {
			continue
		}
		if explicit[name] {
			out.Kept = append(out.Kept, name)
			continue
		}
		out.Disable = append(out.Disable, name)
	}
	return out
}

// HarnessConvergence is the desired-state pass over generated instruction
// files.
type HarnessConvergence struct {
	// Regenerate names files that are stale and unmodified.
	Regenerate []string `json:"regenerate,omitempty"`
	// Reported names files that are stale AND hand-edited, which are
	// left alone and named instead.
	Reported []string `json:"reported,omitempty"`
}

// HarnessFile is one file's drift state, as the check reports it.
type HarnessFile struct {
	// Path is the generated file.
	Path string
	// Stale reports that a fresh generation would differ.
	Stale bool
	// Modified reports that the managed block was hand-edited.
	Modified bool
}

// ConvergeHarnesses decides which generated files a reconverge rewrites.
//
// Stale AND unmodified is the only case that is rewritten. A file the
// operator edited is never silently overwritten (Z/S-53.T3) — it is
// reported, and they decide. A file that is merely stale, its managed
// block untouched, is cascade's own output and is refreshed.
func ConvergeHarnesses(files []HarnessFile) HarnessConvergence {
	out := HarnessConvergence{}
	for _, f := range files {
		if !f.Stale {
			continue
		}
		if f.Modified {
			out.Reported = append(out.Reported, f.Path)
			continue
		}
		out.Regenerate = append(out.Regenerate, f.Path)
	}
	return out
}

// setOf builds a membership set.
func setOf(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}
