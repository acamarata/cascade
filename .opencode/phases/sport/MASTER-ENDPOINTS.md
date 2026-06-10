# MASTER-ENDPOINTS.md — Cascade HTTP API Endpoints

**Purpose:** Registry of every HTTP API endpoint served by cascade-daemon.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09 (E-03 complete)
**Source:** Cascade P3/P4 plan

## Daemon IPC (Unix socket — `~/.cascade/daemon.sock`)

**IPC routing:** typed JSON-RPC (ADR-P3-001) — two-path dispatch in `handle_connection`:
legacy 8-method `Request` enum tried first; unknown methods/fields fall through to
`try_typed_dispatch` which validates via `cascade_types::ipc::deserialize_request`.
Implemented in `crates/cascade-daemon/src/ipc.rs` (T-P3-E00-01, resolves E07-11/E07-12).

| IPC Method | Description | Status | Phase |
|---|---|---|---|
| ping | Liveness check | ✅ Done | P2 |
| status | Daemon status | ✅ Done | P2 |
| context-get | Retrieve context entry | ✅ Done | P2 |
| context-set | Store context entry | ✅ Done | P2 |
| context-list | List context keys | ✅ Done | P2 |
| context-delete | Delete context entry | ✅ Done | P2 |
| subscribe | Subscribe to event bus | ✅ Done | P2 |
| unsubscribe | Unsubscribe from event bus | ✅ Done | P2 |

| Endpoint | Method | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| /api/ping | GET | Daemon liveness check; returns {ok:true} | 🔲 Planned | P3 | T-P3-E01-* |
| /api/gci/rules | GET | GCI rules list from ~/.claude/CLAUDE.md | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/references | GET | GCI references directory listing | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/hooks | GET | CC hooks from settings.json | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/hooks-write | POST | Update CC hooks in settings.json | ✅ Done | P3 | T-P3-E02-26 |
| /api/gci/hooks-write/:hook_id | DELETE | Delete a specific CC hook by id | ✅ Done | P3 | T-P3-E02-26 |
| /api/gci/memory | GET | GCI memory files list | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/skills | GET | Available skills list | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/agents | GET | Available agents list | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/settings | GET | settings.json snapshot (redacted) | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/rag | GET | RAG index status + stats | 🔲 Planned | P3/P4 | T-P3-E02-*, T-P4-E02-* |
| /api/gci/cascade | GET | cascade binary version + config | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/harness | GET | Active harness detection (CC/OC) | 🔲 Planned | P3 | T-P3-E02-* |
| /api/gci/harness-status | GET | Harness integration status | ✅ Done | P3 | T-P3-E02-28 |
| /api/gci/harness-regenerate | POST | Regenerate harness integration config | ✅ Done | P3 | T-P3-E02-28 |
| /api/personal/ | GET | Personal panel root data | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/threads | GET | CC session thread list | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/ideas | GET | Ideas directory | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/inbox | GET | PCI inbox messages | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/crd | GET | CRD daemon status + recent activity | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/fleet | GET | Claude Fleet/account status | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/fleet-quota | GET | Per-account quota state | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/usage | GET | Token/cost usage summary | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/usage-history | GET | Historical usage aggregates by period | ✅ Done | P3 | T-P3-E02-23 |
| /api/personal/scheduled | GET | Scheduled task list | 🔲 Planned | P3 | T-P3-E02-* |
| /api/personal/account | GET | Account info (email, plan) | 🔲 Planned | P3 | T-P3-E02-* |
| /api/projects | GET | ~/Sites project enumeration | 🔲 Planned | P3 | T-P3-E02-* |
| /api/projects/:slug/repos | GET | Repos within a project | 🔲 Planned | P3 | T-P3-E02-* |
| /api/projects/:slug/phase | GET | Active PEWS phase state | 🔲 Planned | P3 | T-P3-E02-* |
| /api/projects/:slug/scaffold | POST | Scaffold PEWS structure | 🔲 Planned | P3 | T-P3-E02-* |
| /api/chat | POST | SSE chat stream proxied to Gemini pool (localhost:3761); 10-tool catalog in chat_tools.rs | ✅ Done | P3 | T-P3-E02-18 |
| /api/chat/history/:id | GET | Chat session history | 🔲 Planned | P3 | T-P3-E02-* |
| /api/tags | GET | Context/memory tags | 🔲 Planned | P3 | T-P3-E06-* |
| /api/gci/rag-status | GET | RAG/RRF serve status, P3 stub | ✅ Done | P3 | T-P3-E02-29 |

## Tauri Commands (cascade-app — `invoke()`)

| Command | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|
| check_wizard_status | Check if onboarding wizard has been completed | ✅ Done | P3 | T-P3-E03-01 |
| wizard_save_checkpoint | Persist wizard phase + state to disk | ✅ Done | P3 | T-P3-E03-02 |
| wizard_load_checkpoint | Load previously saved wizard checkpoint | ✅ Done | P3 | T-P3-E03-02 |
| wizard_clear_checkpoint | Delete wizard checkpoint (reset) | ✅ Done | P3 | T-P3-E03-02 |
| wizard_mark_complete | Mark wizard as completed in config | ✅ Done | P3 | T-P3-E03-03 |
| detect_gemini_pool | Detect existing Gemini API keys in vault | ✅ Done | P3 | T-P3-E03-04 |
| provider_connect | Connect an AI provider with given credentials | ✅ Done | P3 | T-P3-E03-05 |
| download_local_model | Download a local Ollama model | ✅ Done | P3 | T-P3-E03-06 |
| install_daemon | Install + start cascaded as launchd user agent | ✅ Done | P3 | T-P3-E03-29 |
| scan_global_homes | Scan ~/.claude and related dirs for legacy configs | ✅ Done | P3 | T-P3-E03-14 |
| scan_dev_tree | Scan ~/Sites dev tree for per-project .claude dirs | ✅ Done | P3 | T-P3-E03-15 |
| setup_symlinks | Create ~/.cascade symlinks for harness tool names | ✅ Done | P3 | T-P3-E03-27 |
| cascade_unlink_tool | Remove a symlink for a specific tool | ✅ Done | P3 | T-P3-E03-28 |
| archive_legacy_tools | Archive legacy tool directories to ~/.cascade/archive | ✅ Done | P3 | T-P3-E03-25 |
| read_archive_manifest | Read ~/.cascade/archive/manifest.json | ✅ Done | P3 | T-P3-E03-26 |
| list_archived_tools | List all tools recorded in the archive manifest | ✅ Done | P3 | T-P3-E03-26 |
| archive_preflight | Dry-run archive check: what would be archived | ✅ Done | P3 | T-P3-E03-24 |
| restore_tool | Restore an archived tool from ~/.cascade/archive | ✅ Done | P3 | T-P3-E03-33 |
| read_legacy_content | Read raw content from a scanned legacy file | ✅ Done | P3 | T-P3-E03-17 |
| run_ai_merge | Run AI-assisted merge for a content section | ✅ Done | P3 | T-P3-E03-18 |
| write_cascade_content | Write merged content to cascade config path | ✅ Done | P3 | T-P3-E03-21 |
| detect_merge_conflicts | Detect conflicts in proposed merged content | ✅ Done | P3 | T-P3-E03-19 |
| get_merge_prompts | Get AI merge prompt templates for a section type | ✅ Done | P3 | T-P3-E03-20 |
| cascade_provision_google_start | Start Google OAuth provision flow (returns device code) | ✅ Done | P3 | T-P3-E03-08 |
| cascade_provision_google_status | Poll OAuth provision status | ✅ Done | P3 | T-P3-E03-08 |
| cascade_provision_google_cancel | Cancel in-progress OAuth provision | ✅ Done | P3 | T-P3-E03-08 |
| cascade_pool_register_key | Register an API key in the Gemini pool | ✅ Done | P3 | T-P3-E03-05 |
| cascade_pool_deregister_key | Remove an API key from the Gemini pool | ✅ Done | P3 | T-P3-E03-05 |
| cascade_auto_auth_scan | Scan filesystem for importable auto-auth tokens | ✅ Done | P3 | T-P3-E03-07 |
| cascade_auto_auth_import | Import discovered auto-auth tokens into cascade | ✅ Done | P3 | T-P3-E03-07 |
| cascade_providers_health | Get health status of all configured AI providers | ✅ Done | P3 | T-P3-E03-08,43 |
