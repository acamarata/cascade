#!/usr/bin/env bash
# sign-macos.sh — Apple codesign + notarize helper for Cascade.app
# Purpose: Import Developer ID cert, sign the app bundle with hardened runtime,
#          submit to Apple notarytool, wait for approval, then staple the ticket.
# Inputs:
#   APPLE_CERTIFICATE              - base64-encoded .p12 Developer ID Application cert
#   APPLE_CERTIFICATE_PASSWORD     - passphrase for the .p12
#   APPLE_ID                       - Apple ID email used for notarization
#   APPLE_TEAM_ID                  - 10-char Apple Developer Team ID
#   APPLE_APP_SPECIFIC_PASSWORD    - app-specific password generated at appleid.apple.com
#   APP_BUNDLE_PATH                - path to Cascade.app (default: Cascade.app)
#   DMG_PATH                       - path to Cascade.dmg for notarization (default: Cascade.dmg)
# Outputs: signed + notarized + stapled DMG in place; exits non-zero on any failure.
# Constraints: macOS only; requires Xcode Command Line Tools; called from release.yml.
set -euo pipefail

APP_BUNDLE_PATH="${APP_BUNDLE_PATH:-Cascade.app}"
DMG_PATH="${DMG_PATH:-Cascade.dmg}"

# ── 1. Import certificate into a temporary keychain ──────────────────────────
echo "::group::Import signing certificate"
KEYCHAIN_PATH="$RUNNER_TEMP/cascade-signing.keychain-db"
KEYCHAIN_PASSWORD="$(openssl rand -hex 16)"

# Decode the base64 cert to a .p12 file
CERT_PATH="$RUNNER_TEMP/cascade-cert.p12"
echo "$APPLE_CERTIFICATE" | base64 --decode > "$CERT_PATH"

# Create a temporary keychain and import the cert
security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security set-keychain-settings -lut 21600 "$KEYCHAIN_PATH"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security import "$CERT_PATH" \
  -k "$KEYCHAIN_PATH" \
  -P "$APPLE_CERTIFICATE_PASSWORD" \
  -T /usr/bin/codesign \
  -T /usr/bin/security
security list-keychain -d user -s "$KEYCHAIN_PATH"
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: \
  -s -k "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
echo "::endgroup::"

# ── 2. Resolve the signing identity ──────────────────────────────────────────
echo "::group::Resolve signing identity"
SIGNING_IDENTITY="$(security find-identity -v -p codesigning "$KEYCHAIN_PATH" \
  | grep "Developer ID Application" | head -1 | awk '{print $3}' | tr -d '"')"

if [[ -z "$SIGNING_IDENTITY" ]]; then
  echo "ERROR: No 'Developer ID Application' identity found in imported certificate." >&2
  exit 1
fi
echo "Signing identity: $SIGNING_IDENTITY"
echo "::endgroup::"

# ── 3. Sign the app bundle with hardened runtime ─────────────────────────────
echo "::group::Sign Cascade.app"
ENTITLEMENTS_PATH="$(dirname "$0")/../apps/cascade-app/src-tauri/entitlements.plist"
# Fall back to same-dir entitlements.plist if the relative path doesn't resolve
if [[ ! -f "$ENTITLEMENTS_PATH" ]]; then
  ENTITLEMENTS_PATH="entitlements.plist"
fi

codesign \
  --deep \
  --force \
  --options runtime \
  --entitlements "$ENTITLEMENTS_PATH" \
  --sign "$SIGNING_IDENTITY" \
  --timestamp \
  "$APP_BUNDLE_PATH"
echo "::endgroup::"

# ── 4. Verify the signature before submitting ────────────────────────────────
echo "::group::Verify signature"
codesign --verify --deep --strict "$APP_BUNDLE_PATH"
echo "Signature verification passed."
echo "::endgroup::"

# ── 5. Submit to notarytool and wait ─────────────────────────────────────────
echo "::group::Notarize $DMG_PATH"
if [[ ! -f "$DMG_PATH" ]]; then
  echo "WARNING: $DMG_PATH not found — skipping notarization (sign-only run)." >&2
  exit 0
fi

xcrun notarytool submit "$DMG_PATH" \
  --apple-id "$APPLE_ID" \
  --team-id "$APPLE_TEAM_ID" \
  --password "$APPLE_APP_SPECIFIC_PASSWORD" \
  --wait \
  --timeout 20m
echo "::endgroup::"

# ── 6. Staple the notarization ticket ────────────────────────────────────────
echo "::group::Staple $DMG_PATH"
xcrun stapler staple "$DMG_PATH"
echo "Notarization complete — ticket stapled to $DMG_PATH"
echo "::endgroup::"

# ── 7. Cleanup ───────────────────────────────────────────────────────────────
security delete-keychain "$KEYCHAIN_PATH" || true
rm -f "$CERT_PATH"
