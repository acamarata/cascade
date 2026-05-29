#!/usr/bin/env bash
# uninstall.sh — Remove Claw Fleet components.
#
# Flags:
#   --purge   Also delete ~/.config/claw-fleet/ (config + widget position)
#   --help    Show this message

set -euo pipefail

BIN_DIR="$HOME/bin"
UEBERSICHT_WIDGETS="$HOME/Library/Application Support/Übersicht/widgets"
SWIFTBAR_PLUGINS="$HOME/Library/Application Support/SwiftBar/Plugins"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
PLIST_LABEL="io.clawfleet.refresh"
PLIST_DEST="$LAUNCHD_DIR/${PLIST_LABEL}.plist"
SANITY_DEST="$LAUNCHD_DIR/io.clawfleet.sanity.plist"
CONFIG_DIR="$HOME/.config/claw-fleet"

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
if [ -f "$PLIST_DEST" ]; then
  launchctl unload "$PLIST_DEST" 2>/dev/null || true
  rm -f "$PLIST_DEST"
  ok "LaunchAgent removed (io.clawfleet.refresh)."
else
  skip "LaunchAgent plist not found (io.clawfleet.refresh)."
fi
if [ -f "$SANITY_DEST" ]; then
  launchctl unload "$SANITY_DEST" 2>/dev/null || true
  rm -f "$SANITY_DEST"
  ok "Sanity daemon removed (io.clawfleet.sanity)."
else
  skip "Sanity daemon plist not found (io.clawfleet.sanity)."
fi
OC_DEST="$LAUNCHD_DIR/io.clawfleet.opencode.plist"
if [ -f "$OC_DEST" ]; then
  launchctl unload "$OC_DEST" 2>/dev/null || true
  rm -f "$OC_DEST"
  ok "OpenCode probe removed (io.clawfleet.opencode)."
else
  skip "OpenCode probe plist not found (io.clawfleet.opencode)."
fi
PROXY_DEST="$LAUNCHD_DIR/io.clawfleet.gemini-proxy.plist"
if [ -f "$PROXY_DEST" ]; then
  launchctl unload "$PROXY_DEST" 2>/dev/null || true
  rm -f "$PROXY_DEST"
  ok "Gemini pooling proxy removed (io.clawfleet.gemini-proxy)."
else
  skip "Gemini pooling proxy plist not found (io.clawfleet.gemini-proxy)."
fi

# ── Binaries ──────────────────────────────────────────────────────────────────
say "Removing binaries..."
for bin in claw-fleet claw-fleet-refresh claw-fleet-gemini-proxy; do
  if [ -e "$BIN_DIR/$bin" ]; then
    rm -f "$BIN_DIR/$bin"
    ok "Removed $BIN_DIR/$bin"
  else
    skip "$bin not found in $BIN_DIR"
  fi
done

# ── Übersicht widget ─────────────────────────────────────────────────────────
say "Removing Übersicht widget..."
WIDGET_DEST="$UEBERSICHT_WIDGETS/claw-fleet.widget"
if [ -e "$WIDGET_DEST" ] || [ -L "$WIDGET_DEST" ]; then
  rm -rf "$WIDGET_DEST"
  ok "Widget removed."
else
  skip "Widget not found."
fi

# ── SwiftBar plugin ───────────────────────────────────────────────────────────
say "Removing SwiftBar plugin..."
PLUGIN_DEST="$SWIFTBAR_PLUGINS/claw-fleet.5s.sh"
if [ -e "$PLUGIN_DEST" ] || [ -L "$PLUGIN_DEST" ]; then
  rm -f "$PLUGIN_DEST"
  ok "SwiftBar plugin removed."
else
  skip "SwiftBar plugin not found."
fi

# ── Config dir (opt-in purge only) ────────────────────────────────────────────
if $PURGE; then
  say "Purging config dir ($CONFIG_DIR)..."
  if [ -d "$CONFIG_DIR" ]; then
    rm -rf "$CONFIG_DIR"
    ok "Config dir removed."
  else
    skip "Config dir not found."
  fi
  say "Note: ~/.claude/usage-cache.json preserved (shared with other tools)."
else
  say "Config preserved. Re-run with --purge to also remove ~/.config/claw-fleet/."
fi

echo ""
say "Claw Fleet uninstalled."
echo ""
