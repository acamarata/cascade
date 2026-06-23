#!/usr/bin/env bash
# build.sh — Build the Cascade Fleet menu-bar app and install to ~/Applications/
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DERIVED_DATA="$SCRIPT_DIR/build/DerivedData"
ARCHIVE="$SCRIPT_DIR/build/CascadeApp.xcarchive"
INSTALL_DIR="$HOME/Applications"
APP_NAME="CascadeApp.app"

echo "=== Cascade Fleet menu-bar builder ==="

# 1. Regenerate the Xcode project from project.yml if xcodegen is available.
if command -v xcodegen &>/dev/null; then
    echo "[1/4] Regenerating Xcode project via xcodegen..."
    xcodegen generate --spec "$SCRIPT_DIR/project.yml" --project "$SCRIPT_DIR"
else
    echo "[1/4] xcodegen not found — using existing CascadeWidget.xcodeproj"
fi

# 2. Archive the CascadeApp target (Release, ad-hoc signed).
echo "[2/4] Building CascadeApp (Release, ad-hoc signing)..."
xcodebuild archive \
  -project "$SCRIPT_DIR/CascadeWidget.xcodeproj" \
  -scheme "CascadeApp" \
  -configuration Release \
  -archivePath "$ARCHIVE" \
  -derivedDataPath "$DERIVED_DATA" \
  CODE_SIGN_IDENTITY="-" \
  CODE_SIGNING_REQUIRED=NO \
  CODE_SIGNING_ALLOWED=NO \
  DEVELOPMENT_TEAM="" 2>&1 | grep -E '(error:|warning:|Build succeeded|FAILED|ARCHIVE)'

echo "[2/4] Archive complete: $ARCHIVE"

# 3. Copy the .app from the archive into ~/Applications/
echo "[3/4] Installing to $INSTALL_DIR/$APP_NAME ..."
mkdir -p "$INSTALL_DIR"
# Remove old copy if present.
rm -rf "$INSTALL_DIR/$APP_NAME"
cp -R "$ARCHIVE/Products/Applications/$APP_NAME" "$INSTALL_DIR/$APP_NAME"
echo "[3/4] Installed: $INSTALL_DIR/$APP_NAME"

# 4. Launch the app.
echo "[4/4] Launching Cascade menu-bar app..."
open "$INSTALL_DIR/$APP_NAME"
sleep 2
echo ""
echo "Running processes:"
pgrep -fl "CascadeApp" || echo "  (no CascadeApp process found — check Console.app)"
echo ""
echo "=== Done. Cascade Fleet icon should appear in the menu bar. ==="
