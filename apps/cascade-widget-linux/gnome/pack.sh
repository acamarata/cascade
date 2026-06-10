#!/usr/bin/env bash
set -euo pipefail

# Navigate to the script's own directory
cd "$(dirname "$0")"

# Remove old zip
rm -f cascade-quota-widget@acamarata.zip

# Create zip: first the individual files, then the icons directory recursively
zip cascade-quota-widget@acamarata.zip \
  extension.js \
  metadata.json \
  prefs.js \
  stylesheet.css

zip -r cascade-quota-widget@acamarata.zip icons/

# Print confirmation
echo "Packed: cascade-quota-widget@acamarata.zip"
