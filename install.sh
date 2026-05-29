#!/usr/bin/env bash
# cascade install.sh — unified installer for cascade-gemini-proxy + cascade-dashboard
# TODO: populate component installation (T-E12-02 proxy, T-E12-03 dashboard)
set -euo pipefail

INSTALL_DIR="$HOME/.cascade/bin"
LAUNCHD_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"

echo "cascade: installing..."
mkdir -p "$INSTALL_DIR"

# TODO: Install cascade-gemini-proxy (from T-E12-02 merge)
# TODO: Install cascade-dashboard (from T-E12-03 merge)
# TODO: Deploy launchd agents (from T-E12-06 unification)

echo "cascade: done. Run 'launchctl list | grep cascade' to verify."
