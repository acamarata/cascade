#!/usr/bin/env bash
# install.sh — Idempotent installer for Claw Dash.
# Run from the repo root: ./install.sh [flags]
#
# Flags:
#   --no-launchd   Skip LaunchAgent installation
#   --dev          Symlink instead of copy (edits in repo = live update for web/widget)
#   --help         Show this message

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/bin"
UEBERSICHT_WIDGETS="$HOME/Library/Application Support/Übersicht/widgets"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
WEB_DEST="$HOME/.local/share/claw-dash/web"

REFRESH_LABEL="io.clawdash.refresh"
REFRESH_DEST="$LAUNCHD_DIR/${REFRESH_LABEL}.plist"
REFRESH_TEMPLATE="$REPO_DIR/src/launchd/io.clawdash.refresh.plist.template"

SERVER_LABEL="io.clawdash.server"
SERVER_DEST="$LAUNCHD_DIR/${SERVER_LABEL}.plist"
SERVER_TEMPLATE="$REPO_DIR/src/launchd/io.clawdash.server.plist.template"

# ── Defaults ──────────────────────────────────────────────────────────────────
DO_LAUNCHD=true
DEV_MODE=false

for arg in "$@"; do
  case "$arg" in
    --no-launchd) DO_LAUNCHD=false ;;
    --dev)        DEV_MODE=true ;;
    --help|-h)
      grep '^#' "$0" | head -10 | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown flag: $arg" >&2; exit 1 ;;
  esac
done

say() { printf '\033[1;36m==> \033[0m%s\n' "$*"; }
ok()  { printf '\033[0;32m    ✓ \033[0m%s\n' "$*"; }
warn(){ printf '\033[1;33m    ! \033[0m%s\n' "$*"; }
die() { printf '\033[0;31mERROR: \033[0m%s\n' "$*" >&2; exit 1; }

# ── 1. Sanity checks ──────────────────────────────────────────────────────────
say "Checking prerequisites..."

[[ "$(uname)" == "Darwin" ]] || die "Claw Dash requires macOS."

python3 --version >/dev/null 2>&1 || die "python3 is required. Install via: brew install python3"

bash_ver="${BASH_VERSINFO[0]:-0}"
(( bash_ver >= 3 )) || die "bash >= 3.2 required (you have: $BASH_VERSION)"

ok "macOS, python3, bash >= 3.2 all present."

if ! open -Ra "Übersicht" 2>/dev/null; then
  warn "Übersicht not found. Install with: brew install --cask ubersicht"
  warn "Widget install will be skipped."
fi

# ── 2. Install binaries ───────────────────────────────────────────────────────
say "Installing binaries to $BIN_DIR..."
mkdir -p "$BIN_DIR"

# Bin scripts ALWAYS copy (never symlink), even in --dev mode. launchd cannot
# cross volume boundaries without Full Disk Access, so symlinks to /Volumes/*
# die silently with "Operation not permitted". Use claw-dash-sync after
# editing the repo to re-copy the scripts.
cp "$REPO_DIR/src/bin/claw-dash"        "$BIN_DIR/claw-dash"
cp "$REPO_DIR/src/bin/claw-dash-server" "$BIN_DIR/claw-dash-server"
cp "$REPO_DIR/src/bin/claw-dash-sync"   "$BIN_DIR/claw-dash-sync"
chmod +x "$BIN_DIR"/claw-dash*
ok "Binaries copied (launchd can't follow symlinks across volumes)."

# Record repo path so claw-dash-sync can find the source after edits.
mkdir -p "$HOME/.config/claw-dash"
echo "$REPO_DIR" > "$HOME/.config/claw-dash/repo-path"

# ── 3. Web files ──────────────────────────────────────────────────────────────
say "Installing web files to $WEB_DEST..."
mkdir -p "$(dirname "$WEB_DEST")"
if $DEV_MODE; then
  if [ -L "$WEB_DEST" ] && [ "$(readlink "$WEB_DEST")" = "$REPO_DIR/src/web" ]; then
    ok "Web dir already symlinked (dev mode)."
  else
    rm -rf "$WEB_DEST"
    ln -sf "$REPO_DIR/src/web" "$WEB_DEST"
    ok "Web dir symlinked (dev mode): $WEB_DEST -> $REPO_DIR/src/web"
  fi
else
  rm -rf "$WEB_DEST"
  cp -R "$REPO_DIR/src/web" "$WEB_DEST"
  ok "Web files copied to $WEB_DEST"
fi

# ── 4. Übersicht widget ───────────────────────────────────────────────────────
say "Installing Übersicht widget..."
if ! open -Ra "Übersicht" 2>/dev/null; then
  warn "Übersicht not found — skipping widget install."
else
  mkdir -p "$UEBERSICHT_WIDGETS"
  WIDGET_DEST="$UEBERSICHT_WIDGETS/claw-dash.widget"
  if $DEV_MODE; then
    if [ -L "$WIDGET_DEST" ] && [ "$(readlink "$WIDGET_DEST")" = "$REPO_DIR/src/widget/ubersicht/claw-dash.widget" ]; then
      ok "Widget already symlinked (dev mode)."
    else
      ln -sfn "$REPO_DIR/src/widget/ubersicht/claw-dash.widget" "$WIDGET_DEST"
      ok "Widget symlinked (dev mode)."
    fi
  else
    rm -rf "$WIDGET_DEST"
    cp -R "$REPO_DIR/src/widget/ubersicht/claw-dash.widget" "$WIDGET_DEST"
    ok "Widget copied."
  fi
fi

# ── 5. LaunchAgents ───────────────────────────────────────────────────────────
if $DO_LAUNCHD; then
  say "Installing LaunchAgent ($REFRESH_LABEL)..."
  mkdir -p "$LAUNCHD_DIR"

  # Render refresh template (periodic poller, every 60s)
  sed \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@BIN@|$BIN_DIR|g" \
    "$REFRESH_TEMPLATE" > "$REFRESH_DEST"
  ok "Rendered plist to $REFRESH_DEST"

  launchctl unload "$REFRESH_DEST" 2>/dev/null || true
  launchctl load   "$REFRESH_DEST"
  ok "LaunchAgent loaded ($REFRESH_LABEL, every 60s)."

  # Render server template (persistent process, KeepAlive)
  say "Installing LaunchAgent ($SERVER_LABEL)..."
  sed \
    -e "s|@HOME@|$HOME|g" \
    -e "s|@BIN@|$BIN_DIR|g" \
    "$SERVER_TEMPLATE" > "$SERVER_DEST"
  ok "Rendered plist to $SERVER_DEST"

  launchctl unload "$SERVER_DEST" 2>/dev/null || true
  launchctl load   "$SERVER_DEST"
  ok "LaunchAgent loaded ($SERVER_LABEL, persistent / KeepAlive)."

  # Add Übersicht to Login Items so the widget host auto-starts on boot
  if open -Ra "Übersicht" 2>/dev/null; then
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

# ── 6. Seed cache ─────────────────────────────────────────────────────────────
say "Seeding cache (first run may take a few seconds)..."
"$BIN_DIR/claw-dash" 2>/dev/null || warn "Cache seed failed. It will populate on the next 60s tick."

# ── 7. Done ───────────────────────────────────────────────────────────────────
echo ""
say "Claw Dash installed."
echo ""
echo "  What was installed:"
echo "    - Binaries: ~/bin/claw-dash  ~/bin/claw-dash-server  ~/bin/claw-dash-sync"
echo "    - Web files: $WEB_DEST"
echo "    - Übersicht widget: $UEBERSICHT_WIDGETS/claw-dash.widget"
echo "    - LaunchAgent: $REFRESH_LABEL (polls every 60s)"
echo "    - LaunchAgent: $SERVER_LABEL (persistent HTTP server)"
echo ""
echo "  Usage:"
echo "    - Open Übersicht (menu bar) — the Claw Dash widget should appear."
echo "    - Or open http://localhost:$(cat "$HOME/.claw-dash/port" 2>/dev/null || echo "3077") directly in any browser."
echo "    - Drag the widget by its header to reposition."
echo ""
echo "  To manually refresh: ~/bin/claw-dash"
echo "  To sync repo edits:  ~/bin/claw-dash-sync"
echo ""
