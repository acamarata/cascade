#!/usr/bin/env bash
# Generates ClawFleet.xcodeproj from project.yml using XcodeGen.
#
# Run this whenever project.yml changes. The .xcodeproj is gitignored;
# this script is the source of truth for the project structure.
#
# Prerequisites:
#   brew install xcodegen
#
# After running:
#   open ClawFleet.xcodeproj
#   Set your signing team in both target settings, then build and run.
#
# Note: Signing cannot be automated here because it requires a valid
# Apple Developer identity. Set CODE_SIGN_IDENTITY and DEVELOPMENT_TEAM
# in Xcode's "Signing & Capabilities" tab, or pass them to xcodebuild
# directly once you have a team ID.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if ! command -v xcodegen &>/dev/null; then
    echo "xcodegen not found. Install it with: brew install xcodegen" >&2
    exit 1
fi

echo "Generating ClawFleet.xcodeproj..."
xcodegen generate

echo ""
echo "Done. Open the project with:"
echo "  open ClawFleet.xcodeproj"
echo ""
echo "Before building, set your signing team in Xcode:"
echo "  Select the ClawFleetMenuBar target -> Signing & Capabilities -> Team"
echo "  Repeat for the ClawFleetWidget target."
