#!/usr/bin/env bash
set -euo pipefail

# Alias to the KDE packaging script
cd "$(dirname "$0")/../kde"
exec bash pack.sh "$@"
