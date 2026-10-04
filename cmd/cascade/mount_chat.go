// Purpose: registers the `chat` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "chat", Order: 240, Mount: mountChatCmd})
