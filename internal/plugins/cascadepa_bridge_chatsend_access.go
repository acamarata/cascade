package plugins

import "github.com/acamarata/cascade/internal/rpc"

// Purpose: bridge.send's access class (contract:caller-principal). Only the
//   owner's own process may push text to the owner's paired chat, so a session
//   or other principal token is refused KindPermissionDenied before the
//   handler (and so before any transport call) runs. Kept in its own file so
//   the declaration is greppable beside the method it marks.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-access (ADD) — P1-CPA-01.

var _ = rpc.DeclareAccess(MethodBridgeSend, rpc.AccessOwnerOnly)
