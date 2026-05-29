#!/usr/bin/env bash
# cascade uninstall.sh — removes cascade-gemini-proxy + cascade-dashboard launchd agents
# TODO: populate component removal (T-E12-06)
set -euo pipefail

LAUNCHD_DIR="$HOME/Library/LaunchAgents"

echo "cascade: uninstalling..."

# TODO: launchctl unload io.cascade.gemini-proxy
# TODO: launchctl unload io.cascade.dashboard
# TODO: rm -f "$LAUNCHD_DIR/io.cascade.gemini-proxy.plist"
# TODO: rm -f "$LAUNCHD_DIR/io.cascade.dashboard.plist"
# TODO: rm -rf "$HOME/.cascade/"

echo "cascade: uninstalled."
