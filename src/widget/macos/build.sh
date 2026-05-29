#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DERIVED_DATA="$SCRIPT_DIR/build/DerivedData"
ARCHIVE="$SCRIPT_DIR/build/ClawDash.xcarchive"

echo "Building ClawDash..."
xcodebuild archive \
  -project "$SCRIPT_DIR/ClawDashWidget.xcodeproj" \
  -scheme "ClawDashApp" \
  -configuration Release \
  -archivePath "$ARCHIVE" \
  -derivedDataPath "$DERIVED_DATA" \
  CODE_SIGN_IDENTITY="-" \
  CODE_SIGNING_REQUIRED=NO \
  CODE_SIGNING_ALLOWED=NO \
  DEVELOPMENT_TEAM="" 2>&1 | tail -20

echo "Exporting..."
xcodebuild -exportArchive \
  -archivePath "$ARCHIVE" \
  -exportPath "$SCRIPT_DIR/build" \
  -exportOptionsPlist "$SCRIPT_DIR/ExportOptions.plist" 2>&1 | tail -10

echo "Done: $SCRIPT_DIR/build/ClawDash.app"
