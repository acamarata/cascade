// Claw Fleet — Übersicht desktop widget.
// Reads from $CLAW_FLEET_CACHE (default: ~/.claude/usage-cache.json).
// Maintained by claw-fleet-refresh every 5 min. Redraws every 5s. No extra API calls.

export const refreshFrequency = 5000;

// Just cat the cache file. Übersicht calls render() every refreshFrequency
// regardless of whether output changed, so Date.now() inside render gives us
// the live tick. The daemon (claw-fleet-refresh LaunchAgent) is the only
// thing that writes this file; the widget is a pure reader.
export const command = "cat /Users/admin/.claude/usage-cache.json 2>&1 || echo CAT_FAILED:$?";

// ─── helpers ─────────────────────────────────────────────────────────────────
const GRID = 170;
const GAP = 16;
const SNAP_THRESHOLD = 18;

const pct = (u, k) => (u && u[k] && u[k].utilization != null ? u[k].utilization : null);
const at  = (u, k) => (u && u[k] ? u[k].resets_at : 0);
const color = (p) => {
  if (p == null) return "#6B7280";
  if (p >= 100)  return "#9CA3AF";
  if (p >= 76)   return "#E5484D";
  if (p >= 51)   return "#F76808";
  if (p >= 26)   return "#F5A623";
  return "#30A46C";
};
// "5p" / "10a" — hour + a/p, right-padded so single-digit aligns under 2-digit.
const fmtHour = (e) => {
  if (!e) return "—";
  const d = new Date(e * 1000);
  const h24 = d.getHours();
  const h = h24 === 0 ? 12 : h24 > 12 ? h24 - 12 : h24;
  const ap = h24 < 12 ? "a" : "p";
  return `${h}${ap}`.padStart(3, " "); // nbsp keeps monospace alignment in HTML
};
// "5/23 10a" / "5/24  8p" — compact numeric date + space + 3-char right-aligned
// hour. The 3-char hour width keeps single-digit hours visually aligned with
// two-digit hours in monospaced text (" 8p" vs "10p").
const fmtDayHour = (e) => {
  if (!e) return "—";
  const d = new Date(e * 1000);
  return `${d.getMonth() + 1}/${d.getDate()} ${fmtHour(e)}`;
};
// " (66h)" — duration left-padded so "(" aligns across 1/2/3-digit hour counts.
const fmtDurPrefix = (hours) => `(${hours}h)`.padStart(6, " ");
// "H:MM" countdown to a future timestamp. 5-hour max so always fits 4 chars.
const fmtCountdown = (ts) => {
  if (!ts) return "0:00";
  const rem = Math.max(0, Math.floor(ts - Date.now()/1000));
  const h = Math.floor(rem / 3600);
  const m = Math.floor((rem % 3600) / 60);
  return h + ":" + String(m).padStart(2, "0");
};
const fmtPct = (p) => (p == null ? "—" : `${Math.round(p)}%`);
const fmtExtra = (e) => {
  if (!e || !e.is_enabled) return "—";
  const lim  = e.monthly_limit || 0;
  const used = e.used_credits  || 0;
  if (!lim) return `${Math.round(used / 100)}`;
  const rem = (lim - used) / 100;
  if (rem <= 0) return rem < 0 ? `-${Math.abs(rem).toFixed(0)}` : "0";
  if (rem < 1)  return rem.toFixed(2);
  return `${Math.round(rem)}`;
};
const hoursUntil = (e) => e ? Math.max(0, Math.round((e - Date.now()/1000) / 3600)) : null;

// ─── transition tracking (module-level, persists across renders) ─────────────
// Tracks previous cap state per account so we can flash + beep on flip.
const _prevCap = {};         // account → { fiveH: bool, week: bool }
const _flashUntil = {};      // account → { cls, until_ms }

const playSound = (kind) => {
  // Unavail: "Basso" (dud). Avail: "Glass" (ping).
  if (typeof run === "function") {
    const file = kind === "unavail" ? "Basso" : "Glass";
    try { run(`afplay /System/Library/Sounds/${file}.aiff >/dev/null 2>&1 &`); } catch (e) {}
  }
};

// Called with current capped state per account; fires sound + flash on transitions.
const detectTransitions = (accountsWithState) => {
  const now = Date.now();
  accountsWithState.forEach(({ account, fiveH, week }) => {
    const prev = _prevCap[account];
    _prevCap[account] = { fiveH, week };
    if (!prev) return; // first observation — no transition
    // Week cap takes priority over 5h for the flash kind.
    if (prev.week !== week) {
      playSound(week ? "unavail" : "avail");
      _flashUntil[account] = { cls: week ? "flash-unavail" : "flash-avail", until: now + 2500 };
    } else if (prev.fiveH !== fiveH) {
      playSound(fiveH ? "unavail" : "avail");
      _flashUntil[account] = { cls: fiveH ? "flash-unavail" : "flash-avail", until: now + 2500 };
    }
  });
};

const getFlashClass = (account) => {
  const f = _flashUntil[account];
  if (!f) return "";
  if (Date.now() > f.until) { delete _flashUntil[account]; return ""; }
  return f.cls;
};

// ─── state ────────────────────────────────────────────────────────────────────
// Drag uses delta math: on DRAG_START we snapshot the cursor + widget positions;
// on DRAG_MOVE we compute new widget pos = start_widget + (cursor - start_cursor).
// This is immune to coordinate-origin drift between getBoundingClientRect and CSS.

export const initialState = {
  top: 120, left: 40,
  dragging: false, initialized: false,
  dragStartMouseX: 0, dragStartMouseY: 0,
  dragStartTop: 0, dragStartLeft: 0,
  output: "", error: null,
};

export const updateState = (event, prev) => {
  // Log every event so we can see what Übersicht dispatches. Stored in state
  // so the render can display it.
  const tag = `${event && event.type ? `type=${event.type}` : "notype"} keys=[${event ? Object.keys(event).join(",") : "null"}]`;
  const lastEvent = tag;

  // Match the command-result event by presence of output/error. Be liberal in
  // accepting either typed or typeless variants.
  if (event && (Object.prototype.hasOwnProperty.call(event, "output") || Object.prototype.hasOwnProperty.call(event, "error"))) {
    return { ...prev, output: event.output, error: event.error, lastEvent };
  }
  if (event.type === "INIT_POS_IF_NEEDED") {
    if (prev.initialized) return prev;
    return { ...prev, top: event.top, left: event.left, initialized: true, lastEvent };
  }
  if (event.type === "DRAG_START") {
    return {
      ...prev, dragging: true,
      dragStartMouseX: event.mouseX, dragStartMouseY: event.mouseY,
      dragStartTop: prev.top, dragStartLeft: prev.left,
    };
  }
  if (event.type === "DRAG_MOVE" && prev.dragging) {
    const dx = event.mouseX - prev.dragStartMouseX;
    const dy = event.mouseY - prev.dragStartMouseY;
    return { ...prev, top: prev.dragStartTop + dy, left: prev.dragStartLeft + dx };
  }
  if (event.type === "DRAG_END" && prev.dragging) {
    const vw = (typeof window !== "undefined" && window.innerWidth)  || 1440;
    const vh = (typeof window !== "undefined" && window.innerHeight) || 900;
    const WIDGET_W = 345;
    const MIN_VIS  = 40;
    let x = prev.left, y = prev.top;
    if (x < -(WIDGET_W - MIN_VIS)) x = -(WIDGET_W - MIN_VIS);
    if (x > vw - MIN_VIS)          x = vw - MIN_VIS;
    if (y < 0)                     y = 0;
    if (y > vh - MIN_VIS)          y = vh - MIN_VIS;

    const gridX = Math.round(x / (GRID + GAP)) * (GRID + GAP);
    const gridY = Math.round(y / (GRID + GAP)) * (GRID + GAP);
    const snapX = Math.abs(gridX - x) < SNAP_THRESHOLD ? gridX : x;
    const snapY = Math.abs(gridY - y) < SNAP_THRESHOLD ? gridY : y;

    const cmd = `mkdir -p ~/.config/claw-fleet && ` +
                `printf '{"top":%d,"left":%d}' ${snapY} ${snapX} > ` +
                `~/.config/claw-fleet/widget-position.json`;
    if (typeof run === "function") { try { run(cmd); } catch (e) {} }
    return { ...prev, top: snapY, left: snapX, dragging: false };
  }
  return prev;
};

// ─── render ──────────────────────────────────────────────────────────────────
export const render = (props, dispatch) => {
  const safeProps = props || {};
  const { output, error } = safeProps;
  const top  = typeof safeProps.top  === "number" ? safeProps.top  : 120;
  const left = typeof safeProps.left === "number" ? safeProps.left : 40;

  // DEBUG: dump all the keys Übersicht actually sends so we can see what's here.
  const debugKeys = Object.keys(safeProps).join(", ") || "(no keys)";
  const debugOutputType = typeof output;
  const debugOutputPreview = output == null ? "null/undef" : String(output).slice(0, 80);

  if (error) {
    return <div className="shell" style={{ top: `${top}px`, left: `${left}px` }}>
      <div className="hdr"><span className="title">Claw Fleet</span></div>
      <div className="err">⚠ cmd error: {String(error)}</div>
    </div>;
  }

  let data = {}, parseErr = null;
  try { data = JSON.parse(output || "{}"); } catch (e) { parseErr = e.message; }

  const accounts = (data.accounts || []).slice().sort((a, b) =>
    (a.account || "").localeCompare(b.account || "")
  );
  const now = Math.floor(Date.now() / 1000);
  const queriedAgoSec = data.queried_at ? Math.max(0, now - data.queried_at) : null;
  // Color-coded freshness so the user immediately sees if the backend stalled:
  // fresh (<2min) = subtle, warming (<4min) = amber, stale (>=4min) = red.
  const freshnessColor =
    queriedAgoSec == null ? "#7A7F8A" :
    queriedAgoSec < 120   ? "#7A7F8A" :
    queriedAgoSec < 240   ? "#F5A623" :
                            "#E5484D";
  const freshnessLabel =
    queriedAgoSec == null ? "—" :
    queriedAgoSec < 5     ? "just now" :
    queriedAgoSec < 60    ? `${queriedAgoSec}s ago` :
                            `${Math.floor(queriedAgoSec / 60)}m ago`;

  // Position read from disk is deferred to a future iteration — for now the
  // widget uses in-memory state only (resets on Übersicht restart).

  const onMouseDown = (e) => {
    if (!dispatch) return;
    e.preventDefault();
    dispatch({ type: "DRAG_START", mouseX: e.clientX, mouseY: e.clientY });
    const move = (ev) => dispatch({ type: "DRAG_MOVE", mouseX: ev.clientX, mouseY: ev.clientY });
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      dispatch({ type: "DRAG_END" });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  return (
    <div className="shell" style={{ top: `${top}px`, left: `${left}px` }}>
      <div className="hdr" onMouseDown={onMouseDown}>
        <span className="title">Claw Fleet</span>
        <span className="sub" style={{ color: freshnessColor }}>
          <span className="pulse" style={{ background: freshnessColor }}></span>
          {freshnessLabel}
        </span>
      </div>

      {parseErr && <div className="err">parse err: {parseErr}</div>}

      {accounts.length === 0 ? (
        <div className="empty">No accounts in cache yet.</div>
      ) : (() => {
      // Show the EX column only when at least one row has visible extra-credit
      // data. Right now everyone reports null and the column is just six dashes
      // wide; reclaim the space for the rest of the row.
      const showEx = accounts.some(a => fmtExtra(a.usage && a.usage.extra_usage) !== "—");
      return (
      <table>
        <thead>
          <tr>
            <th className="c-idx">#</th>
            <th className="c-num">Ses</th>
            <th className="c-num">Wk</th>
            {/* M/S = Monthly OR Sonnet, depending on the row's provider.
                Claude (A1..A4) shows Sonnet-only weekly%. OpenCode (O1)
                repurposes this slot for Monthly% (Go plan $60 30-day cap).
                Codex (C1) and Gemini (GP) leave it null. */}
            <th className="c-num" title="Monthly% for OpenCode rows; Sonnet weekly% for Claude rows; not used by Codex / Gemini.">M/S</th>
            {showEx && <th className="c-num">Ex</th>}
            <th className="c-num">5H</th>
            <th className="c-num">Wk Rst</th>
          </tr>
        </thead>
        <tbody>
          {(() => {
            // Label-prefix per provider: claude → "A" (Anthropic), codex → "C" (Codex).
            // Anthropic rows render as A1..A4 (per-account); Codex C1, OpenCode O1.
            // Gemini renders as "GP" (Gemini Pool) — the openclaw-io project
            // backing OpenCode's Free Pool (GFP) and Pro Pool (GPP). Pool, not
            // per-account, so no numeric suffix.
            const PROVIDER_PREFIX = { claude: "A", codex: "C", gemini: "GP", opencode: "O" };
            const PROVIDER_ORDER  = ["claude", "codex", "gemini", "opencode"];

            // Auto-hide cancelled-subscription Claude accounts: when an
            // ~/.claude-accN OAuth credential has been revoked the poll never
            // succeeds (last_pull_at stays null and every usage slot is null).
            // The user wants those rows removed cleanly and to reappear on
            // their own once the sub is renewed. Other providers always show.
            const isAutoHidden = (a) => {
              if ((a.provider || "claude") !== "claude") return false;
              const u = a.usage || {};
              const hasUsage = u.five_hour || u.seven_day || u.seven_day_sonnet;
              return a.last_pull_at == null && !hasUsage;
            };

            // Group accounts by provider, preserving each group's internal order.
            const byProvider = {};
            accounts.filter(a => !isAutoHidden(a)).forEach(a => {
              const p = a.provider || "claude";
              (byProvider[p] = byProvider[p] || []).push(a);
            });

            // Build row data + compute cap state per account.
            const rowGroups = PROVIDER_ORDER
              .filter(p => byProvider[p] && byProvider[p].length)
              .map(provider => ({
                provider,
                rows: byProvider[provider].map((a, localIdx) => {
                  const u = a.usage || {};
                  const lp = a.last_pull_at || a.queried_at || 0;
                  // Suppress the stale `*` while an account is in active
                  // rate-limit back-off — we're INTENTIONALLY not probing it
                  // to let Anthropic's throttle window drain. The 30-min
                  // unconditional threshold catches genuine failure modes
                  // (auth_expired, network down, etc.) that back-off can't fix.
                  const inBackoff = (a.back_off_until || 0) > now;
                  const stale = lp ? (now - lp > 1800 && !inBackoff) : false;
                  const s  = pct(u, "five_hour");
                  const w  = pct(u, "seven_day");
                  const sn = pct(u, "seven_day_sonnet");
                  const sAt = at(u, "five_hour");
                  const wAt = at(u, "seven_day");
                  // OpenCode emits per-slot status ("ok" / "rate-limited").
                  // Treat it as the authoritative cap signal when present,
                  // falling back to utilization >= 100 for providers (Claude,
                  // Codex, Gemini) that don't emit a per-slot status.
                  const slotStatus = (slot) => (u && u[slot] && u[slot].status) || null;
                  const fiveHCapped = slotStatus("five_hour")  === "rate-limited" || (s || 0) >= 100;
                  const weekCapped  = slotStatus("seven_day")  === "rate-limited" || (w || 0) >= 100;
                  const prefix = PROVIDER_PREFIX[provider] != null ? PROVIDER_PREFIX[provider] : "";
                  // Pull the suffix from the account name ("claude-acc3" -> 3)
                  // so A1/A2 keep their slot when A3 is auto-hidden, and a
                  // returning A3 reappears as A3 instead of being renumbered.
                  // Gemini: pool concept — no per-account number; label is "GP".
                  const acctSuffix = (a.account || "").match(/(\d+)$/);
                  const label = provider === "gemini"
                    ? prefix
                    : `${prefix}${acctSuffix ? acctSuffix[1] : localIdx + 1}`;
                  // Dot semantics: green = ok with real numbers, amber = key valid but
                  // no quota data (quota_opaque), red = auth/api error, gray = unknown.
                  // Some probes (claude, codex) record their state in `last_error`
                  // rather than `status`, so we check both. rate_limit_error is
                  // expected back-off state with carry-forward — not an alert.
                  const st = a.status || "ok";
                  const errAuth = ["no_auth", "auth_expired", "auth_invalid"];
                  const isAuthFail =
                    errAuth.includes(st) || st === "api_error" ||
                    errAuth.includes(a.last_error);
                  const dotColor =
                    isAuthFail ? "#E5484D" :
                    a.quota_opaque ? "#F5A623" :
                    (st === "ok" || a.last_error === "rate_limit_error" || !a.last_error) && s != null ? "#30A46C" :
                    "#4A4D54";
                  // Build the row tooltip. OpenCode rows get the full Go-plan
                  // context (dollars, balance, useBalance, unblock path); other
                  // providers get the compact one-liner.
                  let rowTitle = `${a.email || a.account} • ${a.provider || "claude"} • ${a.status || "live"}`;
                  if (a.provider === "opencode" && a.opencode_meta) {
                    const m = a.opencode_meta;
                    const plan = (m.subscription_plan || "Go").charAt(0).toUpperCase() + (m.subscription_plan || "go").slice(1);
                    const bal = (m.balance || 0) / 100;
                    const lim = m.limits_usd || {};
                    const sp  = m.spent_usd  || {};
                    const fmt = (spent, limit, st) => {
                      if (spent == null || limit == null) return "—";
                      const base = `$${Math.round(spent)} / $${limit}`;
                      return st === "rate-limited" ? base + " RATE-LIMITED" : base;
                    };
                    const lines = [
                      `${a.email || a.account} — OpenCode ${plan} plan`,
                      `5h:  ${fmt(sp.rolling, lim.rolling, u.five_hour && u.five_hour.status)}`,
                      `7d:  ${fmt(sp.weekly,  lim.weekly,  u.seven_day && u.seven_day.status)}`,
                      `mo:  ${fmt(sp.monthly, lim.monthly, u.seven_day_sonnet && u.seven_day_sonnet.status)}`,
                      `Zen balance: $${bal.toFixed(2)} • useBalance: ${m.use_balance ? "on" : "off"}`,
                    ];
                    if (weekCapped) {
                      if (bal > 0 && !m.use_balance) {
                        lines.push("Unblock: toggle Use balance ON in console.");
                      } else if (bal === 0) {
                        lines.push("Unblock: top up Zen balance + enable Use balance.");
                      }
                    }
                    lines.push("(M/S column = Monthly% for OpenCode rows.)");
                    rowTitle = lines.join("\n");
                  }
                  return { a, label, u, stale, s, w, sn, sAt, wAt, fiveHCapped, weekCapped, dotColor, rowTitle };
                }),
              }));

            // Transitions + sound fire across all accounts regardless of provider.
            const allRows = rowGroups.flatMap(g => g.rows);
            detectTransitions(allRows.map(r => ({ account: r.a.account, fiveH: r.fiveHCapped, week: r.weekCapped })));

            // Render rows. All inter-row separators use the default gray
            // border-top from `tbody td`. Only the Claude→Codex boundary
            // gets the visually-stronger white border, via .boundary-strong.
            const out = [];
            rowGroups.forEach((group, gi) => {
              const prevProvider = gi > 0 ? rowGroups[gi - 1].provider : null;
              const strongBoundary = prevProvider === "claude" && group.provider === "codex";
              group.rows.forEach(({ a, label, u, stale, s, w, sn, sAt, wAt, fiveHCapped, weekCapped, dotColor, rowTitle }, ri) => {
                const rowOpacity = weekCapped ? 0.28 : fiveHCapped ? 0.6 : 1;
                const flashClass = getFlashClass(a.account);
                const boundaryClass = (ri === 0 && strongBoundary) ? "boundary-strong" : "";
                const rowClass = [flashClass, boundaryClass].filter(Boolean).join(" ");
                out.push(
                  <tr key={a.account} className={rowClass} style={{ opacity: rowOpacity }}>
                    <td className="c-idx" title={rowTitle}>
                      <span className="row-dot" style={{ background: dotColor }}></span>
                      {label}
                      {/* Only render the stale * when the dot is healthy-green —
                          a red/amber dot already conveys "something off", and
                          stacking glyphs makes the c-idx cell wrap. */}
                      {stale && dotColor === "#30A46C" ? <span className="stale">*</span> : ""}
                    </td>
                    <td className="c-num" style={{ color: color(s) }}>{fmtPct(s)}</td>
                    <td className="c-num" style={{ color: color(w), fontWeight: weekCapped ? 700 : 500 }}>{fmtPct(w)}</td>
                    <td className="c-num" style={{ color: color(sn) }}>{fmtPct(sn)}</td>
                    {showEx && <td className="c-num money">{fmtExtra(u.extra_usage)}</td>}
                    <td className="c-num mono-pre" style={{ color: fiveHCapped ? "#FFFFFF" : "#6B7280", fontWeight: fiveHCapped ? 600 : 400 }}>
                      {/* Show the 5h countdown whenever sAt is set. The prior
                          weekCapped→"—" gate assumed week-cap means 5h is moot,
                          which is wrong for providers (OpenCode, etc.) with
                          independent rolling windows. */}
                      {sAt ? fmtCountdown(sAt) : "—"}
                    </td>
                    <td className="c-num w-reset mono-pre" style={{ fontWeight: weekCapped ? 700 : 400 }}>
                      {(() => {
                        const h = hoursUntil(wAt);
                        if (h === null) return null;
                        // Blocked rows render at row-opacity 0.28 — at the dark
                        // .hrs color the (Xh) becomes invisible. Use the bright
                        // variant so the prefix stays readable through the dim.
                        const cls = weekCapped ? "hrs hrs-bright" : "hrs";
                        return <span className={cls}>{fmtDurPrefix(h)} </span>;
                      })()}
                      {fmtDayHour(wAt)}
                    </td>
                  </tr>
                );
              });
            });
            return out;
          })()}
        </tbody>
      </table>
      );
      })()}
    </div>
  );
};

// className is a static string. Position is applied inline via render's <div style>.
export const className = `
  top: 0;
  left: 0;
  width: 0;
  height: 0;
  z-index: 1;
  user-select: none;
  font-family: -apple-system, "SF Pro Text", "Helvetica Neue", system-ui;
  -webkit-font-smoothing: antialiased;

  .shell {
    position: absolute;
    /* 420px so the W RESET column can hold "(Xh) May DD HHa" on one line
       even when the (Xh) prefix is 3 digits. At 345px the date+time
       wrapped to two lines on every row. */
    width: 420px;
    color: #E6E6E6;
    background: rgba(14, 16, 24, 0.76);
    backdrop-filter: blur(20px) saturate(1.6);
    -webkit-backdrop-filter: blur(20px) saturate(1.6);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 18px;
    padding: 12px 14px 10px;
    box-shadow:
      0 10px 40px rgba(0,0,0,0.45),
      inset 0 1px 0 rgba(255,255,255,0.05);
  }

  .hdr {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 8px;
    cursor: grab;
  }
  .hdr:active { cursor: grabbing; }
  .title { font-weight: 600; font-size: 12px; letter-spacing: 0.02em; }
  .sub   { color: #7A7F8A; font-size: 10px; font-weight: 400; display: inline-flex; align-items: center; gap: 5px; }
  /* Pulse dot — animated indicator that the widget is alive even when the
     numbers themselves aren't changing. Color is set inline based on cache
     freshness (gray <2min, amber <4min, red >=4min). */
  .pulse {
    display: inline-block;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    animation: cf-pulse 1.6s ease-in-out infinite;
  }
  @keyframes cf-pulse {
    0%, 100% { opacity: 0.35; transform: scale(0.85); }
    50%      { opacity: 1.0;  transform: scale(1.0); }
  }

  /* Static per-row health dot in the label cell. No animation — distinct from
     the header pulse. green=ok+numbers, amber=key valid/quota opaque, red=auth error. */
  .row-dot {
    display: inline-block;
    width: 4px;
    height: 4px;
    border-radius: 50%;
    margin-right: 3px;
    vertical-align: middle;
    opacity: 0.75;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-variant-numeric: tabular-nums;
    font-size: 10.5px;
    font-family: "SF Mono", "Menlo", monospace;
  }
  thead th {
    color: #7A7F8A;
    font-weight: 500;
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    padding: 4px 4px 6px;
    text-align: right;
  }
  thead th.c-idx { text-align: left; }
  tbody td {
    padding: 4px 4px;
    border-top: 1px solid rgba(255,255,255,0.10);
    text-align: right;
  }
  tbody tr:first-child td { border-top: none; }
  /* width fits a 4px health dot + 2-char label like "A1" + optional stale * on
     healthy-green rows. 16px was too tight once the dot landed (wrap on C1). */
  .c-idx   { color: #7A7F8A; text-align: left !important; font-weight: 500; width: 30px; white-space: nowrap; }
  .money   { color: #C7CBD1; font-weight: 500; }
  .w-reset { color: #C7CBD1; }
  /* (Xh) prefix on every row — match the dim look of blocked-row text by
     using a darker base color so it reads as quiet metadata, not a value. */
  .w-reset .hrs { color: #4A4D54; }
  /* Blocked rows render with row-level opacity:0.28, which crushes the dark
     .hrs color into invisibility. Override with the value-text color so the
     prefix remains readable through the row dimming. */
  .w-reset .hrs.hrs-bright { color: #C7CBD1; }
  .stale   { color: #E5484D; font-weight: 700; margin-left: 1px; }
  .mono-pre { white-space: pre; }
  /* Strong boundary between Claude and Codex (A4 to C1). All other row
     separators use the default subtle gray from the tbody td rule above. */
  tr.boundary-strong td { border-top: 1px solid rgba(255,255,255,0.45); }

  /* Flash on avail/unavail transition — runs once for ~2s then fades out. */
  tbody tr { transition: opacity 400ms ease; }
  @keyframes cf-flash-unavail {
    0%   { background: rgba(229, 72, 77, 0.55); }
    40%  { background: rgba(229, 72, 77, 0.30); }
    100% { background: transparent; }
  }
  @keyframes cf-flash-avail {
    0%   { background: rgba(48, 164, 108, 0.55); }
    40%  { background: rgba(48, 164, 108, 0.30); }
    100% { background: transparent; }
  }
  tr.flash-unavail td { animation: cf-flash-unavail 2.2s ease-out 1; }
  tr.flash-avail   td { animation: cf-flash-avail   2.2s ease-out 1; }
  .err     { color: #FB7185; padding: 8px; font-size: 11px; }
  .empty   { color: #9CA3AF; padding: 8px; font-size: 11px; font-style: italic; }
  code     { background: rgba(255,255,255,0.08); padding: 1px 4px; border-radius: 3px; font-family: "SF Mono", Menlo, monospace; font-size: 10px; }
`;
