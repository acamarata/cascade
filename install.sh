#!/usr/bin/env bash
# cascade install.sh — unified installer for cascade-gemini-proxy + cascade-dashboard
#
# Installs:
#   - cascade-gemini-proxy (Gemini API relay, localhost:3761) as io.cascade.gemini-proxy
#   - cascade-dash-server  (fleet dashboard, localhost:9761)  as io.cascade.dashboard
#   - cascade-refresh      (data poller, every 15s)           as io.cascade.refresh
#
# All agents run as user launchd agents (no admin required).
# Install destination: ~/.cascade/bin/
# Log destination:     ~/Library/Logs/cascade-*.log
#
# Usage:
#   ./install.sh          — install or upgrade
#   ./install.sh --dry-run — show what would be installed without doing it

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$HOME/.cascade/bin"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
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

say "Installing cascade to $INSTALL_DIR ..."
[[ $DRY_RUN -eq 1 ]] && say "(dry-run mode — no files written)"

# ---------------------------------------------------------------------------
# 1. Create install directory
# ---------------------------------------------------------------------------
run "mkdir -p \"$INSTALL_DIR\""
run "mkdir -p \"$LOG_DIR\""

# ---------------------------------------------------------------------------
# 2. Install bin scripts
# ---------------------------------------------------------------------------
BINS=(
  cascade-gemini-proxy
  cascade-dash-server
  cascade-dash-sync
  cascade-refresh
  cascade-sanity
  cascade-watchdog
  cascade
  cascade-codex
  cascade-gemini
  cascade-opencode
  cascade-sync
)

for bin in "${BINS[@]}"; do
  src="$SCRIPT_DIR/src/bin/$bin"
  dst="$INSTALL_DIR/$bin"
  if [[ -f "$src" ]]; then
    run "install -m 755 \"$src\" \"$dst\""
    say "  installed $bin"
  else
    say "  WARN: $src not found — skipping"
  fi
done

# ---------------------------------------------------------------------------
# 3. Install web assets for cascade-dash-server
# ---------------------------------------------------------------------------
WEB_DST="$HOME/.local/share/cascade-dash/web"
run "mkdir -p \"$WEB_DST\""
for f in app.js index.html style.css; do
  src="$SCRIPT_DIR/src/web/$f"
  if [[ -f "$src" ]]; then
    run "install -m 644 \"$src\" \"$WEB_DST/$f\""
    say "  installed web/$f"
  fi
done

# ---------------------------------------------------------------------------
# 4. Install launchd plists (unload existing, render template, load)
# ---------------------------------------------------------------------------
install_launchd() {
  local label="$1"
  local template="$2"

  local plist="$LAUNCHD_DIR/${label}.plist"

  # Unload if already loaded (idempotent — ignore errors)
  if launchctl list "$label" &>/dev/null; then
    run "launchctl unload \"$plist\" 2>/dev/null || true"
    say "  unloaded existing $label"
  fi

  # Render template: replace @HOME@ and @BIN@ placeholders
  if [[ $DRY_RUN -eq 0 ]]; then
    sed \
      -e "s|@HOME@|$HOME|g" \
      -e "s|@BIN@|$INSTALL_DIR|g" \
      "$template" > "$plist"
    say "  rendered $plist"
  else
    echo "  [dry-run] render $template -> $plist"
  fi

  # Load
  run "launchctl load \"$plist\""
  say "  loaded $label"
}

install_launchd "io.cascade.gemini-proxy"  "$SCRIPT_DIR/src/launchd/io.cascade.gemini-proxy.plist.template"
install_launchd "io.cascade.dashboard"     "$SCRIPT_DIR/src/launchd/io.cascade.dashboard.plist.template"
install_launchd "io.cascade.refresh"       "$SCRIPT_DIR/src/launchd/io.cascade.refresh.plist.template"

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
say ""
say "Installation complete."
say "  Gemini proxy:  http://localhost:3761"
say "  Dashboard:     http://localhost:9761"
say ""
say "Verify with:"
say "  launchctl list | grep cascade"
say "  curl -s http://localhost:3761/health"
say "  open http://localhost:9761"
