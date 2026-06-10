#!/usr/bin/env bash
set -euo pipefail

# Alias to gnome/pack.sh
cd "$(dirname "$0")/../gnome"
bash ./pack.sh
