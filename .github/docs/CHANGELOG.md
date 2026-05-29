# Changelog

## [0.1.15] - 2026-05-27

### Changed
- Gemini row relabelled **G1 → GP** ("Gemini Pool"). The row represents the openclaw-io GCP project that backs OpenCode's Gemini Free Pool (GFP) and Pro Pool (GPP), not a single per-user account — the pool concept is unitary, so the label intentionally has no numeric suffix.
- Third quota column header renamed **Son → M/S** to reflect its dual semantic per row: **M**onthly% for OpenCode (Go plan $60 30-day cap, in the seven_day_sonnet slot) and **S**onnet weekly% for Claude. Codex and Gemini leave the slot null. The Übersicht header carries a hover tooltip spelling this out.
- OpenCode row tooltip updated to reference the new "M/S column" label.

## [0.1.14] - 2026-05-27

### Added
- **Auto-hide cancelled Claude subscriptions.** Claude accounts whose OAuth credentials have been revoked (last_pull_at == null and every usage slot null) are now filtered out of both widget surfaces instead of rendering as a row of dashes. When a sub is renewed and the next poll succeeds, the row reappears automatically. Other providers always show even when their data is empty so missing data stays visible.
- **Codex OAuth refresh.** `claw-fleet-codex` now refreshes the ChatGPT/OpenAI access token via `auth.openai.com/oauth/token` (client_id `app_EMoamEEZ73f0CkXaXp7hrann`, the Codex CLI public client) when it detects the local token is expired, and retries-and-refreshes on a 401 from the probe. Updated tokens are written back to `~/.codex/auth.json` atomically. Previously a row went blank after ~20 days of CLI inactivity even though the $20/mo ChatGPT Plus subscription was fully active; now it stays populated as long as the refresh_token is valid.
- **Gemini RPD utilization.** Tier 1 daily limit (default 10,000 RPD, overridable via `GEMINI_RPD_LIMIT_OPENCLAW` in vault.env) is now used to compute the 7-day slot percentage. Previously the slot stayed null because Cloud Monitoring's quota/limit doesn't return per-day caps for Tier 1 projects; the published Google AI Studio figure fills the gap. Note also documents that Gemini Advanced consumer subscriptions ($20/mo) do not raise API quotas — those are governed by the GCP project tier separately.

### Changed
- Account labels (A1, A2, C1, G1, O1) now derive their numeric suffix from the underlying account name (`claude-acc3` → `A3`) instead of enumeration order, so when A3 is auto-hidden A1 and A2 keep their slot, and a returning A3 reappears as A3 rather than being renumbered.
- Codex error path now writes `last_error` alongside `error` in the cache so the widget's existing auth-state dot-color logic recognizes the `auth_expired` condition.

## [0.1.13] - 2026-05-24

### Added
- Native desktop panel now replicates macOS's "Dim widgets on desktop = Automatically" behavior. When the desktop is not the active context (you're working in an app), the panel goes monochrome and recedes (saturation 0, opacity 0.55, slight brightness drop); when you click the desktop / Finder becomes frontmost, it animates back to full color. A new `DesktopFocusMonitor` watches `NSWorkspace.didActivateApplicationNotification` and treats Finder-frontmost as desktop-active. Matches how the system widgets it sits beside behave.

### Known limitation
- The "Show Desktop" gesture (F11 / hot corner / swipe) reveals macOS widgets in full color without changing the frontmost app; that signal isn't exposed via public API, so the panel stays dimmed during a transient Show-Desktop peek. Clicking the wallpaper (the common path) works.

## [0.1.12] - 2026-05-24

### Changed
- Native desktop panel now visually matches the macOS desktop widgets it sits beside. Corner radius 22 → 24 px, background retoned from the old dark navy to a neutral charcoal (#1C1C1E, macOS `secondarySystemBackground` dark) over the wallpaper blur, plus a hairline container border like the stock widgets.
- Grid snapping rewritten to the exact macOS desktop-widget grid, measured via CGWindowList: 180 px cells laid out flush (no gap), 8 px left margin from the screen edge, rows starting 38 px below the top. The panel now snaps into the same columns and rows as the system Calendar / Weather widgets instead of the previous 186 px parallel grid.
- Default position aligned to the macOS grid: left column, the row directly beneath the stock widget stack, giving the panel the same 8 px left margin as the system widgets (it previously sat flush at X=0).

## [0.1.11] - 2026-05-24

### Changed
- Native desktop panel now matches the macOS desktop-widget container exactly. Width 345 → 360 px (the measured macOS medium-widget width: system small = 180, medium = 360), and corner radius 12 → 22 px to match the system widgets' rounded container. The prior 12 px radius looked boxy next to the Calendar / Weather widgets. W Rst column minWidth restored to 110 now that 360 px has the room.

## [0.1.10] - 2026-05-24

### Changed
- Native desktop panel width 380 → 345 px to match the macOS medium desktop-widget width, so it tiles cleanly next to Calendar / Notification-Center widgets. The compact column layout (numeric dates, auto-hidden EX) fits at this width; the W Rst column minWidth was trimmed 110 → 100 to suit.

### Added
- Magnetic grid snapping on the native desktop panel, mirroring the Übersicht widget the layout came from: 186 px pitch (170 px cell + 16 px gap = the macOS desktop-widget cell size) with an 18 px snap threshold. The panel snaps to a gridline only when dropped within the threshold, keeping free placement otherwise. Snapping is debounced 0.15 s so dragging stays smooth and snaps on release.

### Fixed
- Saved panel position no longer drifts across launches. The initial position is restored exactly instead of being snapped against `NSScreen.main`, which on a multi-display setup could differ from the display the panel actually settles on. Grid snapping now happens only on a real user drag, where the panel's screen is known.

## [0.1.9] - 2026-05-21

### Added
- OpenCode Go dollar context. Per-window dollar limits ($12 / $30 / $60) are now stored as `opencode_meta.limits_usd` and the computed dollar spend per window as `opencode_meta.spent_usd`. The widget can render "$30 / $30" instead of just "100%".
- OpenCode-specific row tooltips on the Übersicht widget and the native macOS panel. Hover over an OpenCode row label to see: plan name, per-window $ spent / $ limit with explicit RATE-LIMITED tag, Zen balance, useBalance state, and an actionable unblock line ("top up Zen balance + enable Use balance" or "toggle Use balance ON") when the weekly slot is capped. Also clarifies "Son column = monthly% for OpenCode rows."
- SwiftBar plugin detail menu now renders dollar-denominated lines for OpenCode accounts (Session 5h / Week / Monthly with $X / $Y format), a plan line ("OpenCode Go • Zen $0.00 • useBalance off"), and a conditional unblock hint when capped.

### Changed
- Cache `note` for OpenCode rows now reads "go: 5h $0/$12 • 7d $30/$30 rate-limited • mo $30/$60" (with balance appended when non-zero).
- OpenCode docs (.github/docs/limits/opencode.md) updated with the new cache schema, opencode_meta block reference, and v0.1.9 widget tooltip contract.

## [0.1.8] - 2026-05-21

### Added
- OpenCode probe persists the per-slot `status` field ("ok" / "rate-limited") that the dashboard already emits. Stored as `usage.{five_hour,seven_day,seven_day_sonnet}.status` in the cache. Optional; older Anthropic/Codex/Gemini rows leave it unset.
- OpenCode probe persists an `opencode_meta` block: `balance` (cents), `use_balance`, `monthly_limit`, `subscription_plan`, `lite_subscription_id`, `is_admin`. Read directly from the same RSC payload so no extra request is needed.
- OpenCode `note` field now reads as plain English: `"go: 5h 0% • 7d 100% rate-limited • month 50%"` instead of the previous developer hint.

### Changed
- Cap detection across all three surfaces (Übersicht widget, native menu-bar panel, SwiftBar plugin) now treats `status == "rate-limited"` as the authoritative cap signal, falling back to `utilization >= 100` for providers that don't emit a per-slot status. Catches the case where OpenCode flips a slot to rate-limited before the rounded percentage actually reaches 100.
- Native widget no longer hides the 5h countdown when the weekly slot is capped. For providers like OpenCode the rolling window runs independently of the weekly cap; the Übersicht widget already showed it; the native panel now matches.
- SwiftBar plugin labels the third quota slot as "Monthly" for OpenCode rows and "Sonnet week" for Anthropic rows — matches the actual semantic of the value.
- SwiftBar plugin shows "RATE-LIMITED" instead of "CAPPED" on the weekly line for OpenCode when the slot explicitly reports rate-limited.

## [0.1.7] - 2026-05-21

### Changed
- Compact column headers in both surfaces: SES → Ses, WEEK → Wk, SONN → Son, W Reset → Wk Rst.
- Numeric date format: "May 23 10a" becomes "5/23 10a" with a 3-char right-padded hour so single-digit and two-digit hours align vertically in monospaced text.

### Added
- Hide the EX column when every visible row reports no extra-credit data. Reclaims the space rather than rendering a column of "—" placeholders. Applies to the Übersicht widget, the native menu-bar panel, and the native desktop window.

## [0.1.1] - [0.1.6] - 2026-04-22 to 2026-05-20

### Added
- Native macOS surfaces: `ClawFleet.app` ships a MenuBarExtra menu-bar dropdown and a borderless desktop panel pinned at the desktop-icon window level. Single shared `CacheStore` drives both views off one 60-second poll loop.
- Codex (C1), Gemini (G1), and OpenCode Go (O1) providers. Each gets its own label prefix and is sorted into the table behind Anthropic (A1-A4).
- Watchdog and hourly sanity-check daemons.
- `quota-state.json` artifact for downstream consumers (matches the `/aa` skill schema).

### Changed
- 5-hour countdown stays visible when the weekly slot is capped; the redundant stale "*" was removed.
- Desktop window uses `CGWindowLevelForKey(.desktopIconWindow)` instead of `.desktopWindow` so it sits behind app windows but is still draggable.
- Widget panel widened (345 → 420 px Übersicht, 380 → 440 px native) and `Wk Rst` cell `.lineLimit(1)` to stop the column wrapping into two lines.
- Status dot reflects auth-expired errors (red) even when the last successful pull data is still cached.

### Fixed
- Phantom `.lock` and `.tmp` entries no longer leak into the cache from the `~/.claude-acc*` glob.
- `pull_succeeded` now accepts any 200-OK probe regardless of `five_hour` payload, so accounts that haven't burned a session aren't marked stale.
- Codex 429 responses now record the `x-codex-*` headers instead of going to dashes when capped.
- Codex column width and Swift cache decoder mismatch on sanity-block anomalies.

## [0.1.0] - 2026-04-21

### Added
- Initial public release, extracted from personal tooling.
- Übersicht desktop widget (345px, drag-to-position, grid-snap, position persistence).
- SwiftBar menu-bar plugin with per-account detail submenus.
- Auto-detection of Claude Code config directories (`~/.claude` + `~/.claude-acc*`).
- Optional config override at `~/.config/claw-fleet/config.json`.
- LaunchAgent for 5-minute cache refresh.
- Preserves prior cache on API failure (staleness indicator).
- Automatic OAuth token refresh when expired.
