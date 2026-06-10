# MASTER-RPC.md — Cascade IPC / Tauri RPC Methods

**Purpose:** Registry of every Tauri IPC command and daemon RPC method in Cascade.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P3 plan

| Command | Type | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| get_providers | Tauri IPC | Get AI provider list + status from daemon | 🔲 Planned | P3 | T-P3-E01-*, T-P3-E05-* |
| add_provider_key | Tauri IPC | Store API key for a provider in keychain | 🔲 Planned | P3 | T-P3-E05-* |
| remove_provider | Tauri IPC | Remove a provider and its keychain entry | 🔲 Planned | P3 | T-P3-E05-* |
| test_provider | Tauri IPC | Test connectivity to an AI provider | 🔲 Planned | P3 | T-P3-E05-* |
| get_daemon_status | Tauri IPC | Get daemon health + uptime via IPC | 🔲 Planned | P3 | T-P3-E01-* |
| open_project | Tauri IPC | Open a project directory in the OS file browser | 🔲 Planned | P3 | T-P3-E01-* |
| get_window_state | Tauri IPC | Get/set Tauri window size, position | 🔲 Planned | P3 | T-P3-E01-* |
| get_project_graph | Tauri IPC | Scan ~/Sites for cascade dirs; return project/repo/app graph (GraphData) | ✅ Done | P3 | T-P3-E07-12 |
| get_cascade_tier_tree | Tauri IPC | Return 6-tier cascade chain (GCI→PAC) with exists flags for a given root | ✅ Done | P3 | T-P3-E07-12 |
| get_pews_dag | Tauri IPC | Read PEWS ticket YAMLs under phase_root; return ticket dependency DAG (GraphData) | ✅ Done | P3 | T-P3-E07-12 |
