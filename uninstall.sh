#!/usr/bin/env bash
# cascade uninstall.sh — removes cascade-gemini-proxy, cascade-dash, and cascade-refresh agents
#
# Unloads:
#   - io.cascade.gemini-proxy (Gemini proxy, port 3761)
#   - io.cascade.dashboard     (fleet dashboard, port 9761)
#   - io.cascade.refresh      (data poller)
#
# Removes:
#   - ~/.cascade/bin/
#   - ~/.local/share/cascade-dash/web/
#   - ~/Library/LaunchAgents/io.cascade.*.plist
#
# Does NOT remove log files from ~/Library/Logs/ — remove those manually.
#
# Usage:
#   ./uninstall.sh          — full uninstall
#   ./uninstall.sh --dry-run — show what would be removed

set -euo pipefail

INSTALL_DIR="$HOME/.cascade/bin"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
WEB_DIR="$HOME/.local/share/cascade-dash/web"
DRY_RUN=0

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
  esac
done

say() { echo "cascade: $*"; }
run() {
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "  [dry-run] $*"
  else
    eval "$*"
  fi
}

say "Uninstalling cascade ..."
[[ $DRY_RUN -eq 1 ]] && say "(dry-run mode — no files removed)"

# ---------------------------------------------------------------------------
# 1. Unload and remove launchd agents
# ---------------------------------------------------------------------------
unload_launchd() {
  local label="$1"
  local plist="$LAUNCHD_DIR/${label}.plist"

  if launchctl list "$label" &>/dev/null; then
    run "launchctl unload \"$plist\" 2>/dev/null || true"
    say "  unloaded $label"
  else
    say "  $label not loaded — skipping unload"
  fi

  if [[ -f "$plist" ]]; then
    run "rm -f \"$plist\""
    say "  removed $plist"
  fi
}

unload_launchd "io.cascade.refresh"
unload_launchd "io.cascade.dashboard"
unload_launchd "io.cascade.gemini-proxy"

# ---------------------------------------------------------------------------
# 2. Remove installed binaries
# ---------------------------------------------------------------------------
if [[ -d "$INSTALL_DIR" ]]; then
  run "rm -rf \"$INSTALL_DIR\""
  say "  removed $INSTALL_DIR"
fi

# Remove ~/.cascade if now empty
if [[ -d "$HOME/.cascade" ]]; then
  if [[ -z "$(ls -A "$HOME/.cascade" 2>/dev/null)" ]]; then
    run "rmdir \"$HOME/.cascade\""
    say "  removed ~/.cascade (was empty)"
  else
    say "  ~/.cascade not empty — leaving in place"
  fi
fi

# ---------------------------------------------------------------------------
# 3. Remove web assets
# ---------------------------------------------------------------------------
if [[ -d "$WEB_DIR" ]]; then
  run "rm -rf \"$WEB_DIR\""
  say "  removed $WEB_DIR"
fi

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
say ""
say "cascade uninstalled."
say "Log files preserved at ~/Library/Logs/cascade-*.log — remove manually if desired."
