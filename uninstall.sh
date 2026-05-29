#!/usr/bin/env bash
# uninstall.sh — Remove Claw Dash components.
#
# Flags:
#   --purge   Also delete ~/.config/claw-dash/ and ~/.local/share/claw-dash/
#   --help    Show this message

set -euo pipefail

BIN_DIR="$HOME/bin"
UEBERSICHT_WIDGETS="$HOME/Library/Application Support/Übersicht/widgets"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
REFRESH_LABEL="io.clawdash.refresh"
REFRESH_DEST="$LAUNCHD_DIR/${REFRESH_LABEL}.plist"
SERVER_LABEL="io.clawdash.server"
SERVER_DEST="$LAUNCHD_DIR/${SERVER_LABEL}.plist"
CONFIG_DIR="$HOME/.config/claw-dash"
DATA_DIR="$HOME/.local/share/claw-dash"

PURGE=false
for arg in "$@"; do
  case "$arg" in
    --purge)    PURGE=true ;;
    --help|-h)
      grep '^#' "$0" | head -8 | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown flag: $arg" >&2; exit 1 ;;
  esac
done

say()  { printf '\033[1;36m==> \033[0m%s\n' "$*"; }
ok()   { printf '\033[0;32m    ✓ \033[0m%s\n' "$*"; }
skip() { printf '\033[0;90m    - \033[0m%s\n' "$*"; }

# ── LaunchAgents ──────────────────────────────────────────────────────────────
say "Removing LaunchAgents..."
if [ -f "$REFRESH_DEST" ]; then
  launchctl unload "$REFRESH_DEST" 2>/dev/null || true
  rm -f "$REFRESH_DEST"
  ok "LaunchAgent removed ($REFRESH_LABEL)."
else
  skip "$REFRESH_LABEL plist not found."
fi

if [ -f "$SERVER_DEST" ]; then
  launchctl unload "$SERVER_DEST" 2>/dev/null || true
  rm -f "$SERVER_DEST"
  ok "LaunchAgent removed ($SERVER_LABEL)."
else
  skip "$SERVER_LABEL plist not found."
fi

# ── Binaries ──────────────────────────────────────────────────────────────────
say "Removing binaries..."
for bin in claw-dash claw-dash-server claw-dash-sync; do
  if [ -e "$BIN_DIR/$bin" ]; then
    rm -f "$BIN_DIR/$bin"
    ok "Removed $BIN_DIR/$bin"
  else
    skip "$bin not found in $BIN_DIR"
  fi
done

# ── Übersicht widget ──────────────────────────────────────────────────────────
say "Removing Übersicht widget..."
WIDGET_DEST="$UEBERSICHT_WIDGETS/claw-dash.widget"
if [ -e "$WIDGET_DEST" ] || [ -L "$WIDGET_DEST" ]; then
  rm -rf "$WIDGET_DEST"
  ok "Widget removed."
else
  skip "Widget not found."
fi

# ── Config + data dirs (opt-in purge only) ────────────────────────────────────
if $PURGE; then
  say "Purging config dir ($CONFIG_DIR)..."
  if [ -d "$CONFIG_DIR" ]; then
    rm -rf "$CONFIG_DIR"
    ok "Config dir removed."
  else
    skip "Config dir not found."
  fi

  say "Purging data dir ($DATA_DIR)..."
  if [ -d "$DATA_DIR" ]; then
    rm -rf "$DATA_DIR"
    ok "Data dir removed."
  else
    skip "Data dir not found."
  fi

  say "Note: ~/.claw-dash/ (cache dir) preserved — remove manually if needed."
else
  say "Config and data preserved. Re-run with --purge to also remove:"
  say "  ~/.config/claw-dash/  and  ~/.local/share/claw-dash/"
fi

echo ""
say "Claw Dash uninstalled."
echo ""
