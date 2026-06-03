#!/usr/bin/env bash
set -euo pipefail

# Navigate to the kde/ directory (where this script is located)
cd "$(dirname "$0")"

# Validate metadata.json before packaging
if ! python3 -c "import json; json.load(open('metadata.json'))" 2>/dev/null; then
  echo "Error: metadata.json is not valid JSON" >&2
  exit 1
fi

# Remove any prior plasmoid file
rm -f cascade-quota-widget.plasmoid

# Create the .plasmoid file (which is a ZIP archive)
# The .plasmoid format requires metadata.json at the root level
zip -r cascade-quota-widget.plasmoid metadata.json contents/

# Print success message
echo "Packed: cascade-quota-widget.plasmoid"
