# User-Auth Gates — Cascade macOS Widget

## GATE-01: Apple Developer Provisioning

**Status:** PENDING — requires user action

**Required for:** distributable (non-ad-hoc) builds, notarization, App Store distribution

**Actions needed:**
- Log in to developer.apple.com with your Apple ID
- Create App Group identifier: `group.io.cascade`
- Create App IDs: `io.cascade.app`, `io.cascade.widgetextension`
- Download provisioning profiles
- Set DEVELOPMENT_TEAM in Xcode signing settings

**Not required for:** local development (ad-hoc signing works without an Apple Developer account)
