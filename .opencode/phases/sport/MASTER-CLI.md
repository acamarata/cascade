# MASTER-CLI.md — Cascade CLI Commands

**Purpose:** Registry of every `cascade` CLI subcommand.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09 (E-03 complete)
**Source:** Cascade P2/P3/P4 plan

| Command | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|
| cascade status | Show daemon/provider/project status | ✅ Done | P2 | T-P2-E03-06 |
| cascade daemon | Start/stop/restart the cascade daemon | 🔲 Planned | P2 | T-P2-E02-* |
| cascade ping | Health ping to running daemon | 🔲 Planned | P2 | T-P2-E02-* |
| cascade config | Read/write cascade config | 🔲 Planned | P2 | T-P2-E02-* |
| cascade completions | Shell completion generation (bash/zsh/fish) | 🔲 Planned | P2 | T-P2-E02-* |
| cascade init | Initialize cascade in a project | 🔲 Planned | P2 | T-P2-E02-* |
| cascade doctor | Diagnose environment issues | 🔲 Planned | P2 | T-P2-E02-* |
| cascade search | Search across projects and context | 🔲 Planned | P2/P3 | T-P2-E02-*, T-P3-E01-* |
| cascade resolve | Resolve instruction cascade for a path | 🔲 Planned | P2/P3 | T-P2-E02-*, T-P3-E01-* |
| cascade memory | View/manage cascade memory entries | 🔲 Planned | P2/P3 | T-P2-E02-* |
| cascade inbox | Check/process PCI inbox messages | 🔲 Planned | P2/P3 | T-P2-E02-* |
| cascade migrate | Migrate config/state from prior version | 🔲 Planned | P2/P3 | T-P2-E02-* |
| cascade migrate-keys | Migrate API keys to keychain | 🔲 Planned | P3 | T-P3-E05-* |
| cascade harness | Show active harness (CC/OC) info | 🔲 Planned | P3 | T-P3-E06-* |
| cascade mcp | MCP server management (start/stop/list) | 🔲 Planned | P4 | T-P4-E03-* |
| cascade plugin | Plugin management (install/remove/list) | 🔲 Planned | P4 | T-P4-E04-* |
| cascade template | Instruction template management | 🔲 Planned | P3 | T-P3-E04-* |
| cascade generate-instructions | Generate instruction files from template | 🔲 Planned | P3 | T-P3-E04-* |
| cascade setup-oc | Configure OpenCode integration | 🔲 Planned | P3 | T-P3-E04-* |
| cascade monitor-oc | Monitor OpenCode session | 🔲 Planned | P3 | T-P3-E04-* |
| cascade rollback | Rollback to prior cascade version | 🔲 Planned | P3 | T-P3-E06-* |
| cascade update | Self-update cascade binary | 🔲 Planned | P3 | T-P3-E06-* |
| cascade restore --tool | Restore a single archived legacy tool from ~/.cascade/archive | ✅ Done | P3 | T-P3-E03-33 |
| cascade uninstall --full \| --keep-cascade | Uninstall cascade (optionally preserve cascade config) | ✅ Done | P3 | T-P3-E03-35..42 |
| cascade dispatch | Dispatch a task to the daemon | 🔲 Planned | P3 | T-P3-E06-* |
| cascade cache | Manage local instruction/context cache | 🔲 Planned | P3 | T-P3-E01-* |
