package plugins

import (
	// Linked for its init(), which registers the antigravity builtin.
	_ "github.com/acamarata/cascade/plugins/antigravity"
)

// Purpose (this file): wire the antigravity plugin into the builtin registry.
// The antigravity plugin self-registers via its own init(), but we must
// import it here to ensure it is linked into the binary when the builtin
// registry is loaded.
// Inputs: plugins/antigravity's compile-time registration.
// Outputs: antigravity listed as a builtin plugin.
// Constraints: no init() function here — the plugin's own init() handles
// registration. This file exists solely to trigger the import.
// SPORT: internal/plugins builtin-antigravity wiring (ADD) — P1-CORE-02.
