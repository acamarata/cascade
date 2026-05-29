#!/usr/bin/env bash
# install.sh — Idempotent installer for Claw Fleet.
# Run from the repo root: ./install.sh [flags]
#
# Flags:
#   --widget-only     Install Übersicht widget only (skip SwiftBar plugin)
#   --swiftbar-only   Install SwiftBar plugin only (skip Übersicht widget)
#   --no-launchd      Skip LaunchAgent installation
#   --dev             Symlink instead of copy (edits in repo = live update)
#   --help            Show this message

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/bin"
UEBERSICHT_WIDGETS="$HOME/Library/Application Support/Übersicht/widgets"
SWIFTBAR_PLUGINS="$HOME/Library/Application Support/SwiftBar/Plugins"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
PLIST_LABEL="io.clawfleet.refresh"
PLIST_DEST="$LAUNCHD_DIR/${PLIST_LABEL}.plist"
PLIST_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.refresh.plist.template"

WATCHDOG_LABEL="io.clawfleet.watchdog"
WATCHDOG_DEST="$LAUNCHD_DIR/${WATCHDOG_LABEL}.plist"
WATCHDOG_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.watchdog.plist.template"

SANITY_LABEL="io.clawfleet.sanity"
SANITY_DEST="$LAUNCHD_DIR/${SANITY_LABEL}.plist"
SANITY_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.sanity.plist.template"

# ── Defaults ──────────────────────────────────────────────────────────────────
DO_WIDGET=true
DO_SWIFTBAR=true
DO_LAUNCHD=true
DEV_MODE=false

for arg in "$@"; do
  case "$arg" in
    --widget-only)   DO_SWIFTBAR=false ;;
    --swiftbar-only) DO_WIDGET=false ;;
    --no-launchd)    DO_LAUNCHD=false ;;
    --dev)           DEV_MODE=true ;;
    --help|-h)
      grep '^#' "$0" | head -12 | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown flag: $arg" >&2; exit 1 ;;
  esac
done

say() { printf '\033[1;36m==> \033[0m%s\n' "$*"; }
ok()  { printf '\033[0;32m    ✓ \033[0m%s\n' "$*"; }
warn(){ printf '\033[1;33m    ! \033[0m%s\n' "$*"; }
die() { printf '\033[0;31mERROR: \033[0m%s\n' "$*" >&2; exit 1; }

install_file() {
  local src="$1"
  local dest="$2"
  local dest_dir
  dest_dir="$(dirname "$dest")"
  mkdir -p "$dest_dir"
  if $DEV_MODE; then
    if [ -L "$dest" ] && [ "$(readlink "$dest")" = "$src" ]; then
      ok "already symlinked: $dest"
    else
      ln -sf "$src" "$dest"
      ok "symlinked: $dest -> $src"
    fi
  else
    if [ -d "$src" ]; then
      rm -rf "$dest"
      cp -R "$src" "$dest"
    else
      cp "$src" "$dest"
    fi
    ok "copied: $dest"
  fi
}

# ── 1. Sanity checks ──────────────────────────────────────────────────────────
say "Checking prerequisites..."

[[ "$(uname)" == "Darwin" ]] || die "Claw Fleet requires macOS."

python3 --version >/dev/null 2>&1 || die "python3 is required. Install via: brew install python3"

bash_ver="${BASH_VERSINFO[0]:-0}"
(( bash_ver >= 3 )) || die "bash >= 3.2 required (you have: $BASH_VERSION)"

ok "macOS, python3, bash >= 3.2 all present."

# ── 2. Keychain access check ─────────────────────────────────────────────────
say "Checking keychain access..."
HAVE_CRED=false
for d in "$HOME"/.claude-acc* "$HOME"/.claude; do
  [ -d "$d" ] || continue
  suffix=$(python3 -c "import hashlib; print(hashlib.sha256('${d}'.encode()).hexdigest()[:8])")
  svc="Claude Code-credentials-${suffix}"
  if security find-generic-password -s "$svc" -w >/dev/null 2>&1; then
    ok "Found credential for $(basename "$d") ($svc)"
    HAVE_CRED=true
    break
  fi
done
if ! $HAVE_CRED; then
  warn "No Claude Code credentials found in keychain."
  warn "Open Claude Code in at least one config dir to populate them, then re-run install.sh."
  warn "Continuing install anyway — cache will populate once credentials are present."
fi

# ── 3. Install binaries ────────────────────────────────────────────────────────
say "Installing binaries to $BIN_DIR..."
mkdir -p "$BIN_DIR"

# Bin scripts ALWAYS copy (never symlink), even in --dev mode. launchd cannot
# cross volume boundaries without Full Disk Access, so symlinks to /Volumes/*
# die silently with "Operation not permitted". Use claw-fleet-sync after
# editing the repo to re-copy the scripts.
cp "$REPO_DIR/src/bin/claw-fleet"          "$BIN_DIR/claw-fleet"
cp "$REPO_DIR/src/bin/claw-fleet-refresh"  "$BIN_DIR/claw-fleet-refresh"
cp "$REPO_DIR/src/bin/claw-fleet-watchdog" "$BIN_DIR/claw-fleet-watchdog"
cp "$REPO_DIR/src/bin/claw-fleet-sanity"   "$BIN_DIR/claw-fleet-sanity"
cp "$REPO_DIR/src/bin/claw-fleet-sync"     "$BIN_DIR/claw-fleet-sync"
cp "$REPO_DIR/src/bin/claw-fleet-gemini"   "$BIN_DIR/claw-fleet-gemini"
cp "$REPO_DIR/src/bin/claw-fleet-gemini-proxy" "$BIN_DIR/claw-fleet-gemini-proxy"
cp "$REPO_DIR/src/bin/claw-fleet-opencode" "$BIN_DIR/claw-fleet-opencode"
chmod +x "$BIN_DIR"/claw-fleet*
ok "Binaries copied (launchd can't follow symlinks across volumes)."

# Record repo path so claw-fleet-sync can find the source after edits.
mkdir -p "$HOME/.config/claw-fleet"
echo "$REPO_DIR" > "$HOME/.config/claw-fleet/repo-path"

# ── 4. Übersicht widget ────────────────────────────────────────────────────────
if $DO_WIDGET; then
  say "Installing Übersicht widget..."
  if ! open -Ra "Übersicht" 2>/dev/null; then
    warn "Übersicht not found. Install with: brew install --cask ubersicht"
    warn "Skipping widget install."
  else
    mkdir -p "$UEBERSICHT_WIDGETS"
    if $DEV_MODE; then
      ln -sfn "$REPO_DIR/src/widgets/ubersicht/claw-fleet.widget" \
              "$UEBERSICHT_WIDGETS/claw-fleet.widget"
      ok "Widget symlinked (dev mode)."
    else
      rm -rf "$UEBERSICHT_WIDGETS/claw-fleet.widget"
      cp -R "$REPO_DIR/src/widgets/ubersicht/claw-fleet.widget" "$UEBERSICHT_WIDGETS/"
      ok "Widget copied."
    fi
  fi
fi

# ── 5. SwiftBar plugin ────────────────────────────────────────────────────────
if $DO_SWIFTBAR; then
  say "Installing SwiftBar plugin..."
  if ! open -Ra "SwiftBar" 2>/dev/null; then
    warn "SwiftBar not found. Install with: brew install --cask swiftbar"
    warn "Skipping SwiftBar plugin install."
  else
    mkdir -p "$SWIFTBAR_PLUGINS"
    install_file "$REPO_DIR/src/widgets/swiftbar/claw-fleet.5s.sh" \
                 "$SWIFTBAR_PLUGINS/claw-fleet.5s.sh"
    chmod +x "$SWIFTBAR_PLUGINS/claw-fleet.5s.sh"
    ok "SwiftBar plugin installed."
  fi
fi

# ── 6. LaunchAgent ────────────────────────────────────────────────────────────
if $DO_LAUNCHD; then
  say "Installing LaunchAgent ($PLIST_LABEL)..."
  mkdir -p "$LAUNCHD_DIR"

  # Render template
  sed \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@BIN@|$BIN_DIR|g" \
    "$PLIST_TEMPLATE" > "$PLIST_DEST"
  ok "Rendered plist to $PLIST_DEST"

  # Unload if already loaded (idempotent)
  launchctl unload "$PLIST_DEST" 2>/dev/null || true
  launchctl load   "$PLIST_DEST"
  ok "LaunchAgent loaded ($PLIST_LABEL, every 60s)."

  # Watchdog — heals cache daemon + Übersicht every 60s
  sed \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@BIN@|$BIN_DIR|g" \
    "$WATCHDOG_TEMPLATE" > "$WATCHDOG_DEST"
  launchctl unload "$WATCHDOG_DEST" 2>/dev/null || true
  launchctl load   "$WATCHDOG_DEST"
  ok "Watchdog loaded ($WATCHDOG_LABEL, every 60s)."

  # Sanity checker — validates cache time-windows + utilization every hour
  sed \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@BIN@|$BIN_DIR|g" \
    "$SANITY_TEMPLATE" > "$SANITY_DEST"
  launchctl unload "$SANITY_DEST" 2>/dev/null || true
  launchctl load   "$SANITY_DEST"
  ok "Sanity daemon loaded ($SANITY_LABEL, every 3600s)."

  # Gemini probe — checks AI Pro session health every 300s
  GEMINI_LABEL="io.clawfleet.gemini"
  GEMINI_DEST="$LAUNCHD_DIR/${GEMINI_LABEL}.plist"
  GEMINI_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.gemini.plist.template"
  if [ -f "$GEMINI_TEMPLATE" ]; then
    sed \
      -e "s|@HOME@|$HOME|g" \
      -e "s|@BIN@|$BIN_DIR|g" \
      "$GEMINI_TEMPLATE" > "$GEMINI_DEST"
    launchctl unload "$GEMINI_DEST" 2>/dev/null || true
    launchctl load   "$GEMINI_DEST"
    ok "Gemini probe loaded ($GEMINI_LABEL, every 300s)."
  else
    warn "Gemini plist template not found — skipping gemini LaunchAgent."
  fi

  # Gemini pooling proxy — runs continuously on port 3761
  PROXY_LABEL="io.clawfleet.gemini-proxy"
  PROXY_DEST="$LAUNCHD_DIR/${PROXY_LABEL}.plist"
  PROXY_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.gemini-proxy.plist.template"
  if [ -f "$PROXY_TEMPLATE" ]; then
    sed \
      -e "s|@HOME@|$HOME|g" \
      -e "s|@BIN@|$BIN_DIR|g" \
      "$PROXY_TEMPLATE" > "$PROXY_DEST"
    launchctl unload "$PROXY_DEST" 2>/dev/null || true
    launchctl load   "$PROXY_DEST"
    ok "Gemini pooling proxy loaded ($PROXY_LABEL, continuous daemon)."
  else
    warn "Gemini proxy plist template not found — skipping gemini-proxy LaunchAgent."
  fi

  # OpenCode probe — checks Go subscription usage every 300s
  OC_LABEL="io.clawfleet.opencode"
  OC_DEST="$LAUNCHD_DIR/${OC_LABEL}.plist"
  OC_TEMPLATE="$REPO_DIR/src/launchd/io.clawfleet.opencode.plist.template"
  if [ -f "$OC_TEMPLATE" ]; then
    sed \
      -e "s|@HOME@|$HOME|g" \
      -e "s|@BIN@|$BIN_DIR|g" \
      "$OC_TEMPLATE" > "$OC_DEST"
    launchctl unload "$OC_DEST" 2>/dev/null || true
    launchctl load   "$OC_DEST"
    ok "OpenCode probe loaded ($OC_LABEL, every 300s)."
  else
    warn "OpenCode plist template not found — skipping opencode LaunchAgent."
  fi

  # Add Übersicht to Login Items so the widget host auto-starts on boot
  if $DO_WIDGET && open -Ra "Übersicht" 2>/dev/null; then
    osascript >/dev/null 2>&1 <<'APPLESCRIPT'
tell application "System Events"
  if not (exists login item "Übersicht") then
    make login item at end with properties {path:"/Applications/Übersicht.app", hidden:false}
  end if
end tell
APPLESCRIPT
    ok "Übersicht added to Login Items (auto-start on boot)."
  fi
fi

# ── 7. Seed cache ─────────────────────────────────────────────────────────────
say "Seeding cache (first run may take 10–20s)..."
"$BIN_DIR/claw-fleet-refresh" 2>/dev/null || warn "Cache seed failed. It will populate on the next 5-min tick."

# ── 8. Done ───────────────────────────────────────────────────────────────────
echo ""
say "Claw Fleet installed."
echo ""
echo "  What happens next:"
echo "    - LaunchAgent runs claw-fleet every 5 minutes automatically."
echo "    - Desktop widget: open Übersicht (menu bar) and the widget should appear."
echo "      Drag it by the header to reposition."
echo "    - Menu bar: open SwiftBar; the ◉ AC1 icon should appear in your menu bar."
echo "    - Data refreshes every 5s from cache, no API calls at that interval."
echo ""
echo "  To manually refresh: claw-fleet-refresh"
echo "  Cache: \${CLAW_FLEET_CACHE:-~/.claude/usage-cache.json}"
echo ""
