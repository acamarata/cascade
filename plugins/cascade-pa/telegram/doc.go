// Package telegram implements the cascade-pa Telegram bridge module
// (P1-E23-W5-S48-T1): the Bot API long-poll loop, the Q-identity
// pairing/allowlist gate, the fail-closed dispatch gate in front of every
// handler, and the default-off module lifecycle.
//
// It never imports internal/** (Art.10.2, R-14.69). Everything it needs from
// the host — the egress firewall, the elevated-verb table, the durable state,
// the paired-device registry, the bot token — arrives through an interface the
// host composition root satisfies (internal/plugins/cascadepa_bridge_wiring.go).
// Every one of those seams has a fail-closed default, so a partially wired host
// refuses rather than admits.
//
// SPORT: plugins/cascade-pa/telegram (ADD) — P1-E23-W5-S48-T1.
package telegram
