# MASTER-WORKFLOWS.md — Cascade GitHub Actions Workflows

**Purpose:** Registry of every GitHub Actions workflow in the cascade repository.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3/P4 plan

| Workflow | File | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| CI (Rust + macOS Widget) | .github/workflows/ci.yml | Build + test cascade Rust workspace (ubuntu, macos, windows) + widget-macos job (WidgetKit, macos-14) | 🟡 Partial | P2 | T-P2-E01-*, T-P2-E04-11 |
| CI (Tauri dashboard) | .github/workflows/ci-tauri.yml | Build + test Tauri 2 app on all platforms | 🔲 Planned | P3 | T-P3-E01-* |
| CI (macOS Widget) | .github/workflows/ci.yml (widget-macos job) | WidgetKit build + test on macos-14 runner; added as job in ci.yml | ✅ Done | P2 | T-P2-E04-11 |
| CI (Linux Widget) | .github/workflows/ci-widget-linux.yml | GNOME Shell Extension + KDE Plasmoid CI | 🔲 Planned | P2 | T-P2-E03-* |
| CI (Windows Widget) | .github/workflows/ci-widget-windows.yml | Windows widget/tray build + test | 🔲 Planned | P2 | T-P2-E05-* |
| Release | .github/workflows/release.yml | Tag-triggered release: build all platforms, sign, upload | 🔲 Planned | P3 | T-P3-E06-* |
| Update manifest | .github/workflows/update-manifest.yml | Publish update manifest to update channel URL | 🔲 Planned | P3 | T-P3-E06-* |
| Integration tests | .github/workflows/integration.yml | cascade-providers integration tests with mock servers | 🔲 Planned | P3 | T-P3-E05-* |
