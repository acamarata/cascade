#!/usr/bin/env bash
# Claw Fleet — SwiftBar menu-bar plugin (refresh every 5s via `.5s.sh` suffix).
# Reads $CLAW_FLEET_CACHE (default: ~/.claude/usage-cache.json). No extra API calls.
# Install: symlink or copy this file to ~/Library/Application Support/SwiftBar/Plugins/

CACHE="${CLAW_FLEET_CACHE:-$HOME/.claude/usage-cache.json}"
REFRESH_BIN="$(dirname "$0")/../../bin/claw-fleet-refresh"
# Resolve real path for refresh action (handles symlinks)
REFRESH_BIN="$(cd "$(dirname "$REFRESH_BIN")" 2>/dev/null && pwd)/$(basename "$REFRESH_BIN")" 2>/dev/null || REFRESH_BIN="$HOME/bin/claw-fleet-refresh"

python3 - "$CACHE" "$REFRESH_BIN" <<'PY'
import json, os, sys, time

CACHE = sys.argv[1]
REFRESH_BIN = sys.argv[2]

# Load cache
data = {}
if os.path.exists(CACHE):
    try:
        data = json.load(open(CACHE))
    except Exception:
        pass

accts = sorted(data.get("accounts", []), key=lambda a: a.get("account", ""))
now = int(time.time())

def pct(u, k):
    return (u or {}).get(k, {}).get("utilization")

def at(u, k):
    return (u or {}).get(k, {}).get("resets_at") or 0

def color(p):
    if p is None: return "gray"
    if p >= 100: return "#111827"
    if p >= 76:  return "red"
    if p >= 51:  return "orange"
    if p >= 26:  return "#F5A623"
    return "#30A46C"

def hms(ts):
    if not ts: return "—"
    return time.strftime("%H:%M", time.localtime(ts))

def dt(ts):
    if not ts: return "—"
    return time.strftime("%b %-d  %H:%M", time.localtime(ts))

def label_for(acct):
    """Derive a short display label from the account dir basename."""
    # e.g. "claude-acc1" -> "AC1", "claude" -> "AC0", "acc2" -> "AC2"
    import re
    m = re.search(r'(\d+)$', acct)
    if m:
        return f"AC{m.group(1)}"
    if acct == "claude":
        return "AC0"
    return acct[:4].upper()

def money_rem(e):
    if not e or not e.get("is_enabled"): return "—"
    lim = e.get("monthly_limit") or 0
    used = e.get("used_credits") or 0
    if not lim: return f"${used/100:.0f}"
    rem = (lim - used) / 100
    if rem <= 0:
        return "⚫ -${:.0f}".format(abs(rem)) if rem < 0 else "⚫ $0"
    if rem < 1: return f"${rem:.2f}"
    return f"${int(round(rem))}"

# ── Menu-bar title ─────────────────────────────────────────────────────────
def slot_status(u, k):
    """Per-slot status string ("ok" / "rate-limited"). OpenCode emits this;
    other providers leave it absent. Used alongside utilization >= 100 as the
    authoritative cap signal."""
    return (u or {}).get(k, {}).get("status") if isinstance((u or {}).get(k), dict) else None

def week_capped(a):
    u = a.get("usage") or {}
    if slot_status(u, "seven_day") == "rate-limited":
        return True
    return (pct(u, "seven_day") or 0) >= 100

weekly = [pct(a.get("usage"), "seven_day") or 0 for a in accts]
capped = sum(1 for a in accts if week_capped(a))
hot = sum(1 for w in weekly if 76 <= w < 100)
max_w = max(weekly, default=0)

if not accts:
    print("☁︎ — | color=gray")
elif capped == len(accts):
    print("⚫ all capped | color=#FB7185")
elif capped > 0:
    print(f"⚫ {capped}  ·  w{int(max_w)}% | color=#FB7185 size=12")
elif hot > 0:
    print(f"🔴 {hot}  ·  w{int(max_w)}% | color=#F5A623 size=12")
else:
    freshest = min(accts, key=lambda a: pct(a.get("usage"), "seven_day") or 999)
    vs = label_for(freshest.get("account", ""))
    print(f"◉ {vs}  ·  {int(max_w)}% | color=#30A46C size=12")

print("---")

# ── Header ────────────────────────────────────────────────────────────────
stamp = time.strftime("%H:%M:%S", time.localtime(data.get("queried_at", now)))
age_s = now - data.get("queried_at", now)
age_str = "just now" if age_s < 60 else f"{age_s//60}m ago"
print(f"◉ Claw Fleet  ·  {age_str} | size=12 color=#6366F1")
print(f"cache updated {stamp} | size=10 color=gray")
print("---")

# ── Per-account blocks ────────────────────────────────────────────────────
for i, a in enumerate(accts, 1):
    acct = a.get("account") or str(i)
    u = a.get("usage") or {}
    vs = label_for(acct)
    email = a.get("email") or acct
    s = pct(u, "five_hour")
    w = pct(u, "seven_day")
    sn = pct(u, "seven_day_sonnet")
    e = u.get("extra_usage") or {}
    lp = a.get("last_pull_at") or a.get("queried_at") or 0
    stale = lp and (now - lp) > 600

    wdisp = f"{int(w)}%" if w is not None else "—"
    sdisp = f"{int(s)}%" if s is not None else "—"
    prefix = f"{i}*" if stale else str(i)
    # OpenCode emits per-slot "rate-limited" status even if percentage rounds
    # below 100 — honor it as the cap signal here too.
    is_capped = (w or 0) >= 100 or slot_status(u, "seven_day") == "rate-limited"
    status = "⚫" if is_capped else ("🔴" if (w or 0) >= 76 else ("🟠" if (w or 0) >= 51 else "🟢"))
    who = email.split("@")[0][:9] if "@" in email else email[:9]
    print(f"{prefix}  {status}  {vs}  ·  {who:<9}  ·  S {sdisp}  W {wdisp} | color={color(w)} size=12 font=Menlo")

    # OpenCode rows get dollar-denominated rows (the Go plan limits the user
    # in $ not %). All other providers get the original percent-only layout.
    oc_meta = a.get("opencode_meta") if a.get("provider") == "opencode" else None
    if oc_meta:
        lim = oc_meta.get("limits_usd") or {}
        sp  = oc_meta.get("spent_usd")  or {}
        def usd_line(window_key, label, pct_val, slot_key, reset_key):
            spent = sp.get(window_key)
            limit = lim.get(window_key)
            st    = slot_status(u, slot_key)
            base  = f"${int(round(spent))}/${limit}" if spent is not None and limit is not None else f"{int(pct_val or 0)}%"
            tail  = " RATE-LIMITED" if st == "rate-limited" else ""
            reset_at = at(u, slot_key)
            reset_part = f"  reset {hms(reset_at)}" if reset_at and (now < reset_at < now + 6*3600) else (f"  reset {dt(reset_at)}" if reset_at else "")
            return f"--  {label:11s}  {base}{tail}{reset_part}"
        print(f"{usd_line('rolling', 'Session 5h', s,  'five_hour',         'five_hour')} | size=11 color={color(s)} font=Menlo")
        print(f"{usd_line('weekly',  'Week total', w,  'seven_day',         'seven_day')} | size=11 color={color(w)} font=Menlo")
        print(f"{usd_line('monthly', 'Monthly',    sn, 'seven_day_sonnet',  'seven_day_sonnet')} | size=11 color={color(sn)} font=Menlo")
        bal  = (oc_meta.get("balance") or 0) / 100
        ub   = oc_meta.get("use_balance") or False
        plan = (oc_meta.get("subscription_plan") or "Go").capitalize()
        print(f"--  Plan         OpenCode {plan} • Zen ${bal:.2f} • useBalance {'on' if ub else 'off'} | size=11 font=Menlo color=#9CA3AF")
        if is_capped and bal == 0 and not ub:
            print("--  ⓘ Unblock: top up Zen balance + enable Use balance | size=11 color=#F5A623 font=Menlo")
        elif is_capped and bal > 0 and not ub:
            print("--  ⓘ Unblock: toggle Use balance ON in console | size=11 color=#F5A623 font=Menlo")
    else:
        print(f"--  Session 5hr      {int(s or 0)}%      reset {hms(at(u,'five_hour'))} | size=11 color={color(s)} font=Menlo")
        if is_capped:
            hrs = max(0, int((at(u,'seven_day') - now) / 3600))
            label = "RATE-LIMITED" if slot_status(u, "seven_day") == "rate-limited" else "CAPPED"
            print(f"--  Week total    {int(w or 0)}%     {label} · reset {dt(at(u,'seven_day'))} ({hrs}h) | size=11 color={color(w)} font=Menlo")
        else:
            print(f"--  Week total    {int(w or 0)}%     reset {dt(at(u,'seven_day'))} | size=11 color={color(w)} font=Menlo")
        print(f"--  Sonnet week   {int(sn or 0)}% | size=11 color={color(sn)} font=Menlo")
        print(f"--  Extra credit  {money_rem(e)}  remaining | size=11 font=Menlo")

    if stale:
        mins = (now - lp) // 60
        print(f"--  ⚠ Data {mins}m stale (rate-limited?) | size=11 color=#FB7185")

    if i < len(accts):
        print("-----")

# ── Footer actions ────────────────────────────────────────────────────────
print("---")
print(f"↻  Refresh now | shell={REFRESH_BIN} terminal=false refresh=true")
print("◨  Open Dashboard | shell=/usr/bin/open param1=-b param2=tracesOf.Uebersicht terminal=false")
PY
