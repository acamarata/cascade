# CHANGELOG

All notable changes to Cascade are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [unreleased]

### Added (P2 E-04: macOS Widget & Menubar)
- Cascade macOS WidgetKit widget (Small/Medium/Large) reading from App Group cache.json
- 30-second refresh timeline with isStale at 120s
- Monochrome-dim when desktop inactive (widgetRenderingMode .accented)
- NSStatusItem menubar companion with green/amber/offline icon states
- POSIX AF_UNIX JSON-RPC client for daemon health (cascade.status)
- Unit tests: CacheModel decode, isStale, ageString (8 tests)
- GitHub Actions CI job (macos-14 runner, xcodegen + xcodebuild test)
- xcodegen project.yml replacing manual Xcode project setup
- USER-AUTH-GATES.md: Apple Developer provisioning documented
