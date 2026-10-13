package plugins

import (
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose: picks the Bot API transport the bridge's telegram module is built
//   over, once. deps.Transport (a byte-level seam for host tests that may not
//   import net/http) wins over deps.HTTPClient; production sets neither and
//   gets the telegram default. Kept out of enabledBridge so that function
//   stays under the funlen cap.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-module (ADD) — P1-CPA-01.

// newBridgeTelegramModule assembles the telegram module for token over the
// transport deps names.
func newBridgeTelegramModule(token string, deps BridgeDeps, gate telegram.EgressGate, stores *cascadepa.Stores,
	journal *bridgeJournal, scanner telegram.SecretScanner) *telegram.TelegramModule {
	if deps.Transport != nil {
		return telegram.NewModuleOver(token, deps.Transport, gate, bridgeElevationPolicy{}, stores, journal, scanner, journal)
	}
	return telegram.NewModule(token, deps.HTTPClient, gate, bridgeElevationPolicy{}, stores, journal, scanner, journal)
}
