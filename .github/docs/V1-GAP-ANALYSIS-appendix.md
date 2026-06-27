
## Appendix — per-pillar findings (evidence)

### fts — mostly

**Exists:**
- crates/cascade-cli/src/cmd/init.rs — CLI-driven .cascade/ directory bootstrap with auto-detection of tier (gci/prc/pac) and dry-run support
- crates/cascade-cli/src/cmd/doctor.rs — comprehensive health diagnostics: tier symlinks, daemon socket, config integrity, audit log chain, security token permissions, auto-fix flag
- crates/cascade-cli/src/cmd/setup_oc.rs — idempotent OpenCode MCP wiring + per-project instructions generation with atomic writes
- apps/cascade-app/src/features/onboarding/ — 10-phase interactive wizard (Welcome, ProviderConnect, ScanLegacy, MergeContent, ToolModes, VerifyDiff, ArchiveLegacy, SymlinkSetup, DaemonInstall, Done)
- apps/cascade-app/src-tauri/src/commands/mod.rs:1082-1161 — install_daemon() Tauri command for macOS (plist generation, atomic write, launchctl load)
- .github/wiki/onboarding-wizard.md — comprehensive setup documentation covering all 10 phases, reversibility, CLI reference, troubleshooting
- install.sh — legacy shell installer for launchd agents (gemini-proxy, dashboard, refresh)
- README.md — quick start guide with installation channels (brew, cargo, winget, snap, flatpak) and basic workflow (init → edit → sync → search)

**Gaps:**
- Non-interactive scriptable setup for Local LLM/GFP agents: no --accept-defaults or headless mode flag for `cascade init` to allow unattended execution; wizard is Tauri-based (GUI-only, no CLI equivalent)
- No bootstrap shell script for agent setup: missing a one-liner e.g. `curl -fsSL https://install.cascade.dev | bash` or similar; install.sh is macOS-specific and shell-based, not cross-platform
- Daemon binary distribution unclear: install_daemon() looks for ~/.local/bin/cascaded but repo does not show how cascaded is built/packaged/installed before wizard runs; unclear if pre-built binaries exist or if agents must cargo build first
- Cross-platform daemon installation incomplete: install_daemon() only implements macOS (launchctl + plist); no Linux systemd/init.d templates or Windows Service implementation visible (mentioned in DaemonInstallPhase code but not implemented)
- Post-install verification step missing: no `cascade verify` or post-init healthcheck command to confirm all tiers readable, daemon socket reachable, providers wired; doctor.rs exists but is read-only diagnostic
- Provider credential storage and OAuth flow stubbed: provider_connect() in mod.rs (line 1003-1030) is a stub; E-04 scope (deferred); local agents cannot authenticate without manual key entry
- CLI init lacks --provider flag: no way to pre-specify provider (e.g., `cascade init --provider gemini --api-key $KEY`) for unattended setup; Tauri wizard drives everything
- No agent-facing MCP context setup: cascade setup-oc wires OpenCode only; no equivalent for Claude Code, Cursor, Aider, or local CLI tools; agents see no context by default post-init
- Wizard state checkpoint format private to app: wizard-state.json is written by Tauri app, not accessible to CLI agents; no CLI command to query wizard progress or force completion
- No automated step-through or test harness: missing a --demo or --test-run mode that executes the wizard programmatically (all 10 phases) without user input for agent validation

**Recommended work:**
- T-FTS-01: Add headless mode to cascade init: --accept-defaults --provider gemini --api-key $KEY for unattended agent-driven setup; auto-tier detection, blank CASCADE.md, skip provider/merge phases
- T-FTS-02: Build cross-platform daemon installer in Rust: Tauri install_daemon() → Linux systemd unit + Windows Service implementation (conditional on OS); unify all three platforms in cascade_install crate
- T-FTS-03: Create curl | bash installer script: publish install.cascade.dev or similar; detects OS, downloads correct cascade binary, runs cascade init --accept-defaults, verifies health
- T-FTS-04: Add cascade verify post-init command: checks .cascade/ dir readable, all 6 tier files exist, daemon socket reachable, resolves context for CWD, exits 0 on success
- T-FTS-05: Wire provider credential CLI flags: cascade init --provider {gemini|anthropic|openai} --api-key $KEY; calls provider_connect during init (requires E-04 OAuth stub completion)
- T-FTS-06: Extend setup-oc to all tools: cascade setup-{claude,cursor,aider,codex} commands that generate tool-specific instructions and MCP wiring; parallel to setup-oc
- T-FTS-07: Expose wizard state to CLI: `cascade wizard status` (reads wizard-state.json) and `cascade wizard complete` (forces completion) for agent introspection
- T-FTS-08: Add --demo/--test mode to wizard: programmatic 10-phase execution without display for CI/integration testing; all Tauri commands mocked or stubbed
- T-FTS-09: Document agent-ready setup workflow: add ~/.github/wiki/Agent-Setup.md explaining init flow, credential injection, provider wiring, MCP context for GFP/local LLM
- T-FTS-10: Validate daemon binary availability before wizard: install_daemon() should check ~/.local/bin/cascaded exists before writing plist; provide download link or fail gracefully

### gfp — partial

**Exists:**
- Google OAuth PKCE client with callback listener (google_oauth.rs: GoogleOAuthClient, start_pkce_flow, await_callback, exchange_code)
- Gemini API key validation (google_oauth.rs: validate_gemini_key probes Gemini 1.5 Flash endpoint)
- GCP project + API key provisioning engine (google_provision.rs: full_auto, guided, manual modes)
- OAuth token refresh (google_oauth.rs: refresh_token_flow)
- Gemini proxy server at localhost:3761 (gemini_proxy.rs: GeminiProxy struct with HTTP handler)
- Round-robin routing table with 429 cooldown (routing_table.rs: RoutingTable with pick_next + mark_rate_limited)
- Keychain-backed API key storage (key_loader.rs: load_api_keys supports 28 slots via keychain)
- Providers store persistence (providers_store.rs: ProvidersStore written to ~/.cascade/providers.json)
- Auto-detection of existing AI harness accounts (auto_auth_import.rs: scans CC, OC, Codex, Cursor, env vars)
- IPC handlers for provisioning (ipc_provision.rs: cascade_provision_google_start/status/cancel)
- UI components for provisioning (GeminiPoolStep mentioned in E-03 W-05 tickets)
- MANUAL mode key registration (handle_provision_google_start supports manual API key input)

**Gaps:**
- No loop for creating MAX keys per account — full_auto hardcoded to n=1 (ipc_provision.rs:119 calls client.full_auto(&email, 1))
- No multi-account provisioning orchestration — wizard provisions one key per Google account, not multiple keys per account
- No auto-retry or resumable multi-key provisioning — single full_auto call, no loop continuation on quota/rate limits
- No dynamic pool scaling — hardcoded 28-slot limit assumed across all Google accounts and projects
- No batch provisioning UI — GeminiPoolStep likely provisions one key per account, not MAX keys upfront
- No quota/billing monitoring for multi-project strategy — no tracking of GCP project creation limits (25 active per account)
- No pool exhaustion detection or proactive key creation — no background daemon task to create additional keys when quota nears limit
- Provisioning parameter n is not used from UI context — no count parameter passed from frontend
- No documented strategy for reaching actual 'MAX' — unclear if 28 is the target or if each of N Google accounts should provision up to 28 keys
- No integration test covering multi-key provisioning workflow

**Recommended work:**
- Implement loop in full_auto provisioning — modify ipc_provision.rs handle_provision_google_start to accept count parameter and call client.full_auto(&email, n) in a loop (1..=desired_count)
- Add multi-key provisioning UI — extend GeminiPoolStep to show 'Create N additional keys' slider or preset buttons (e.g., 5, 10, 28)
- Create ProvisionBatch struct and handler — new ipc handler cascade_provision_google_batch to orchestrate multiple project creations with progress tracking and per-project status polling
- Implement pool exhaustion monitor — daemon background task that polls routing_table slot metrics and triggers key creation when active slots drop below threshold
- Document GCP quota strategy — add wiki page covering 25-project limit per account, recommend creating multiple Google accounts for 150+ keys, rate-limit cooling strategy
- Add multi-account provisioning wizard step — allow import of multiple Google OAuth accounts before provisioning loop, enabling round-robin or sequential key creation across accounts
- Implement resumable provisioning — store provisioning state in ~/.cascade/provisioning-state.json with checkpoint, allow restart if wizard is force-quit during loop
- Add quota estimation UI — show user how many keys will be created, estimated quota cost, number of GCP projects needed (25 per account limit)
- Cover multi-key provisioning in integration tests — wiremock fixture for 5+ sequential project creations, verify all keys stored in keychain
- Update documentation for 'maximize keys' — clarify in wiki/FEATURES.md that GFP is designed for 28+ keys and document the setup procedure

### local-llm — mostly

**Exists:**
- cascade-local-llm crate fully built: crates/cascade-local-llm/ — LocalLlmAdapter implements ProviderAdapter trait
- Three model runners implemented: Gemma-2-2B (default), Llama-3.2-3B, Phi-3-Mini via candle-rs CPU inference with optional Metal GPU acceleration (macOS)
- Model weight downloader with streaming HTTP, SHA-256 verification, disk-space pre-check, resume support — crates/cascade-local-llm/src/downloader.rs
- LocalLlmRunner async inference engine with lazy model load (spawn_blocking) and token streaming via tokio mpsc channels
- Gemma-2 chat-template prompt formatter with system/user/assistant role support
- Configuration system (LocalLlmConfig) with serde serialization for daemon config files
- Error types (LocalModelError) with specific variants for weights-not-found, tokenizer-load, candle failures, disk-space, checksum-mismatch
- Comprehensive unit tests (adapter object-safety, model factories, health checks, downloader fixtures via wiremock)
- ProviderRegistry in cascade-providers pre-wired for 'local' provider with routing table entries (LocalFallback, RagEmbed, RagRerank, Chat, CodeCompletion, CascadeMerge)
- Ollama adapter auto-detection in daemon startup (localhost:11434 probe) — crates/cascade-daemon/src/main.rs lines 185-189
- Provider health-check background task spawned on daemon startup (5-min refresh, non-blocking)

**Gaps:**
- NOT WIRED: cascade-daemon does NOT import or register LocalLlmAdapter; daemon only registers Ollama (T-P3-E04-19) and NoopProvider placeholders (lines 172-182)
- NO setup flow: cascade init wizard has no local-llm model detection, download prompt, or model directory initialization
- NO CLI integration: no 'cascade models download <model-id>' command exists to invoke downloader; users cannot fetch weights from CLI
- NO daemon initialization: LocalLlmAdapter never instantiated; no ProviderRegistry entry for 'local:gemma-2-2b' on daemon startup
- NO onboarding UX: no wizard step to offer local LLM as offline fallback during initial setup
- NO config persistence: LocalLlmConfig structure exists but daemon never loads it from ~/.cascade/config.toml or providers.json
- NO agent task invocation: cannot verify if LocalLlmAdapter.complete() or complete_stream() actually drives agent work (cannot test setup driver scenario)
- IMPORT GAP: cascade-daemon Cargo.toml does not list cascade-local-llm dependency — must add to enable registration
- MODEL REGISTRY: downloader hardcodes 3 models with placeholder SHA-256 hashes (need real checksums for gemma/llama/phi)
- FEATURE FLAG: Metal acceleration exists but untested in CI/CD (default off); Mac ARM builds never validate GPU inference path

**Recommended work:**
- T1 (blocking): Add cascade-local-llm to cascade-daemon Cargo.toml dependencies; import LocalLlmAdapter in main.rs
- T2 (blocking): Wire LocalLlmAdapter registration in daemon startup after Ollama detection — check ~/.cascade/models/gemma-2-2b/ exists, construct LocalLlmConfig, register with 'local:gemma-2-2b' ID
- T3 (blocking): Implement 'cascade models download <model-id>' CLI command (dispatch.rs) — invoke cascade_local_llm::download_model, show progress bar, handle errors
- T4 (setup driver): Add local-llm detection + download prompt to cascade init wizard flow — offer to download model if ~/.cascade/models/ is empty
- T5 (E2E): Create integration test: invoke cascade-daemon with local model, call complete_stream() with a simple prompt, verify token stream flows through ProviderAdapter trait
- T6: Update downloader model registry with real SHA-256 checksums for gemma-2-2b, llama-3.2-3b, phi-3-mini from HuggingFace Hub
- T7: Document local-llm setup in .github/wiki/ — add 'Local LLM Setup.md' page with hardware requirements, download instructions, troubleshooting
- T8: Add local-llm provider to provider settings UI (ProviderSettings.tsx) — show model path, VRAM, available models dropdown, manual download button
- T9: Update daemon config schema (config.toml) to include optional local_llm section with model_path, temperature, max_tokens overrides
- T10 (stretch): Implement Metal feature-gated CI test on macOS ARM runners to validate GPU inference path works

### subs — partial

**Exists:**
- Auto-auth scanner detects 5 harness sources: Claude Code, OpenCode, Codex, Cursor (macOS), env vars (crates/cascade-providers/src/auto_auth_import.rs)
- ProviderRegistry + RoutingTable route tasks across multiple AI providers by capability (crates/cascade-providers/src/registry.rs, adapter.rs)
- 11 concrete ProviderAdapter implementations: Anthropic, OpenAI, Gemini, OpenCode Go, Groq, Mistral, DeepSeek, Cohere, Together, OpenRouter, Ollama + generic OpenAI-compat (crates/cascade-providers/src/adapters/)
- Auto-detected from harness config dirs: CC auth (.claude/), OC auth (.config/opencode/), Codex auth (.codex/ or .config/codex/), Cursor cached email (macOS Library/Application Support/Cursor/)
- ProviderEntry stores harness-keyed accounts with auth_kind (OAuthToken, ApiKey, Unknown), enabled flag, source (auto-detected or manual) in ProvidersStore (crates/cascade-core/src/providers_store.rs)
- Onboarding wizard Phase 2 (Connect Provider) offers: Gemini (OAuth), Anthropic (API key), OpenAI (API key), OpenCode Go (OAuth), local Ollama (no auth) per wiki/onboarding-wizard.md
- Tool.Integration.md documents Cascade-managed file generation for CC, OC, Cursor, Aider, Windsurf, Codex, Antigravity
- Keychain-backed credential storage via cascade-keychain crate for all imported API keys and OAuth tokens (no plaintext on disk)
- P3/E-04 epic (AI Provider Integrations) shipped: adapter trait, OAuth PKCE, cloud + local adapters complete per phase status (32% of P3 done)
- Routing table priority lists per TaskType: CascadeMerge, Chat, CodeCompletion, RagEmbed, RagRerank, LocalFallback (crates/cascade-providers/src/registry.rs lines 56-96)

**Gaps:**
- NO adapter for Cursor IDE — detected in auto-auth (reads cached email from macOS Keychain) but not invokable as a provider; Cursor owns its subscription auth, Cascade can only hint at the identity, not route work to Cursor's inference engine
- NO adapter for Antigravity — documented in Tool-Integration.md as supported for .antigravity/config.json generation, but no provider adapter exists to invoke Antigravity's AI models; tool is file-generation-only, not subscription-routable
- NO adapter for Gemini Free Pool proxy mode mentioned in epic (GFP/GPP toggle in E-04 scope) — code may exist but not routable as a standalone provider subscription; check if gemini adapter handles pooled routing
- OpenCode Go models hardcoded in adapter (kimi-k2, deepseek-v4-pro, gpt-5.5-fast per opencode.rs L76) — no dynamic model discovery; model list static at build time, may become stale when OpenCode releases new subscriptions
- No routing-to-Cursor-subscription capability — Cursor is detected but not routable; users cannot delegate tasks to Cursor's AI backend via Cascade routing engine; P4 scope mentioned in epics but not implemented
- Missing cross-harness subscription negotiation — each harness (CC, OC, Codex) has its own OAuth/API key; Cascade does NOT merge or arbitrate between multiple subscriptions for the same provider (e.g., multiple Claude Code accounts)
- No Gemini Free Pool (GFP) proxy mode detection — E-04 mentions GFP/GPP toggle + localhost:3761 routing, but code does not show detection/routing of user's pooled Gemini access if they set it up
- Antigravity subscription type NOT routable — no adapter, no TaskType → Antigravity fallback chain; Antigravity remains file-generation-only (CLI link --tool antigravity generates config, but Cascade cannot dispatch work to it)

**Recommended work:**
- Create CursorAdapter (P4): listen to Cursor's daemon IPC or read .cursor/session state to detect active subscriptions, route CodeCompletion + Chat tasks to Cursor's embedded Claude engine (currently out-of-scope per E-06 Harness Ecosystem epic)
- Implement AntigravityAdapter (P4): parse .antigravity/config.json, detect Antigravity API endpoint + key, implement ProviderAdapter trait for Antigravity's inference API, add to RoutingTable as fallback for local/offline scenarios
- Add dynamic model discovery for OpenCode Go (P4): replace hardcoded model list with live /v1/models poll on first connection, cache in ProviderRegistry, refresh on health_check to surface newly-enabled subscriptions
- Document Gemini Free Pool proxy mode (P3-E04 refinement): if GFP impl exists, ensure daemon detects pooled setup via localhost:3761 probe and routes Gemini-tasked work through proxy when enabled; if not, defer to P4
- Implement multi-harness subscription aggregation policy (P4): allow Cascade to detect multiple Claude Code accounts / OpenCode sessions / Codex auth, ask user which to prefer per TaskType, store routing choice in settings
- P4/E-06 Harness Ecosystem: extend Codex bridge (task T-P4-E06-04) to include Cursor workspace detection, bidirectional AGENTS.md generation, and Cursor daemon IPC to enable subscription routing
- Add Antigravity detection scanner (P4): mirror auto_auth_import pattern, scan .antigravity/config.json for endpoint + key, return DiscoveredAccount with source=Antigravity, feed into onboarding Phase 2 provider picker
- Validate E-04 coverage against vision (P3 completion gate): audit all ProviderAdapter implementations against P3/E-04 epic success metrics — confirm Cursor routing is explicitly out-of-scope (P4) rather than silently missing

### tiers — mostly

**Exists:**
- cascade-core/src/cascade_resolve.rs (GCI>PCI>APC>PPC>PRC>PAC 6-tier merge engine)
- cascade-core/src/cascade_resolution.rs (ResolvedCascade wrapper with spec-shaped output for E-02)
- cascade-core/src/resolution.rs (Resolver: discovery walk + merge, wraps TierDiscovery)
- cascade-core/src/discovery.rs (TierDiscovery: heuristic tier classification via path walk)
- cascade-types/src/cascade_tier.rs (CascadeTier enum with acronym/description)
- cascade-types/src/tiers.rs (TierName enum with default_path() for GCI/PCI/APC/PPC/PRC/PAC)
- cascade-cli/src/cmd/resolve.rs (cascade resolve command calling Resolver.resolve())
- cascade-cli/src/cmd/generate_instructions.rs (cascade generate-instructions consuming ResolvedCascade)
- Tests in cascade_resolve.rs: all_tiers_found, partial_tiers_only_gci, mcp_server_url_lowest_tier_wins, json_serialization_valid
- Tests in cascade_resolution.rs: 6 comprehensive tier resolution tests
- Documentation: .github/wiki/Six-Tier-Taxonomy.md, Cascade-Resolution.md
- ENV override: CASCADE_APC_PATH supported in TierName::default_path() with tests

**Gaps:**
- PCI tier nomenclature broken: TierName defines PCI as ~/Downloads/.cascade but discovery.rs classifies ~/Sites as Pci — spec shows PCI should be a custom portfolio folder, not a hardcoded path
- Heuristic vs explicit mismatch: resolution.rs walks ancestors assigning tiers sequentially, not matching TierName::default_path() logic — Sites=Pci bug prevents correct APC detection at ~/Sites
- Daemon IPC cascade_resolve is stub only (logs audit event, no implementation) — handler pending E-07+
- Custom APC path selection: CASCADE_APC_PATH env var wired into TierName but not into discovery.rs TierDiscovery (only has test-only with_sites override, no env binding)
- Missing PCI tier mapping in production discovery: if PCI should be ~/Downloads, discovery needs explicit Downloads detection; if portfolio-scoped, discovery heuristic cannot auto-detect user intent

**Recommended work:**
- Ticket: Fix PCI/APC tier classification in discovery.rs (line 151-157) — map ~/Sites -> APC, add ~/Downloads -> PCI explicit checks before heuristic fallback
- Ticket: Integrate CASCADE_APC_PATH env var into TierDiscovery.classify_tier() so non-~/Sites portfolio roots are auto-detected in production code
- Ticket: Implement daemon IPC handler for cascade_resolve (currently stub in crates/cascade-daemon/src/ipc.rs line ~497) — call resolve_cascade_full() and return ResolvedCascade JSON
- Ticket: Add integration test covering all 6 tiers with custom APC_PATH to cascade-core tests (currently only unit tests)
- Ticket: Update discovery.rs module docs to clarify that PCI/APC detection is heuristic-based and depends on ~/Downloads or ~/Sites presence — document the limitation that custom portfolio roots require cascade sync / explicit registration

### symlink — mostly

**Exists:**
- Link/Unlink CLI (crates/cascade-cli/src/cmd/link.rs, unlink.rs) — creates symlinks CLAUDE.md/AGENTS.md → CASCADE.md with tool names (claude, opencode, cursor, aider, codex, continue, windsurf, antigravity); maps tool IDs to sibling filenames
- Symlink core lib (crates/cascade-core/src/symlinks.rs) — create_siblings() and verify_siblings() functions for managing CLAUDE.md/AGENTS.md symlinks
- Tauri command setup_symlinks (apps/cascade-app/src-tauri/src/commands/symlinks.rs) — implements full symlink plan with non-destructive logic (skips real files, replaces existing symlinks, HOME confinement, tests coverage)
- Windows fallback handling (symlinks.ts) — offers Developer Mode prompt or file-copy fallback when symlink creation fails with ACCESS_DENIED
- Generate-instructions harness (crates/cascade-cli/src/cmd/generate_instructions.rs) — writes CLAUDE.md, AGENTS.md (symlink), settings.json MCP entry for CC; opencode-instructions.md + opencode.json for OC; idempotent with cascade header markers
- SymlinkSetupPhase UI (apps/cascade-app/src/features/onboarding/phases/SymlinkSetupPhase.tsx) — preview table, Apply button, per-entry success/error reporting
- Onboarding scanner (T-P3-E03-10) — detects legacy tool homes at ~/.claude, ~/.codex, ~/.opencode, ~/.cursor, ~/.aider, ~/.windsurf, ~/.antigravity (DONE status)
- Path resolution hardcoded to .cascade (crates/cascade-types/src/paths.rs) — all constants use CASCADE_DIR_NAME='.cascade', global_cascade_dir()=~/.cascade, project_cascade_dir()=projectRoot/.cascade, no alternative folder support

**Gaps:**
- NO user folder preference mechanism — init always creates .cascade; no --folder flag, no config option to switch default to .claude/.codex/.opencode; pillar brief explicitly asks for 'user picks .claude / .codex', but implementation does not exist
- NO conditional symlink generation based on user preference — generate_instructions outputs to fixed .claude/CLAUDE.md and per-project opencode.json; does not offer alternatives
- NO UI to select which folder per-project — onboarding wizard E-03-14 (SymlinkSetupPhase) creates symlinks but does not ask user to choose .cascade vs .claude vs .codex before setup
- Path resolution walk-up is hardcoded to .cascade — link.rs, unlink.rs, and generate_instructions.rs all look for .cascade ancestor; would need fallback chains to find .claude/.codex/.opencode if user chose differently
- Config schema has no folder_preference field — cascade-cli/cmd/config.rs and config TOML only track tool modes, not AI folder choice
- No per-tool fallback logic — symlink logic assumes target CASCADE.md exists at fixed .cascade location; if user wanted CASCADE.md in ~/.claude or ~/.codex, entire path resolution breaks
- Harness integration would need path awareness — CC reads ~/.claude/CLAUDE.md first (its hardcoded home); Codex reads ~/.codex/; if Cascade's CASCADE.md lives elsewhere by user choice, harness file generation becomes complex and error-prone

**Recommended work:**
- Ticket: Add --folder / --ai-folder flag to cascade init — accept --folder [.cascade|.claude|.codex] argument; store in .cascade/config.toml under [cascade]ai_folder_choice; auto-detect first run based on what exists (prioritize existing .claude if present)
- Ticket: Extend path resolution to respect folder preference — modify crates/cascade-types/src/paths.rs functions (global_cascade_dir, project_cascade_dir) to check config for user choice; implement fallback chain (.claude → .codex → .opencode → .cascade) on path walk-up
- Ticket: Update onboarding wizard Phase N (pre-Phase 8) with AI Folder Choice dialog — before SymlinkSetupPhase, ask user 'Which folder should I use for CASCADE.md?' with radio options + smart default (detect if .claude or .codex exists); store selection in wizard state, pass to init/setup
- Ticket: Wire folder choice through generate-instructions — pass --ai-folder flag to cascade generate-instructions; write instructions to user-chosen home folder rather than hardcoded .claude/.codex paths
- Ticket: Per-harness output path configuration — extend config schema to allow tools.claude_code.instructions_path and tools.codex.instructions_path; update generate_instructions.rs to read these paths and write CLAUDE.md to user-selected location
- Ticket: Document folder choice strategy in .github/wiki — explain how to switch folders mid-project, how cascade walks up looking for CASCADE.md, clarify that .cascade is default but user can opt into .claude/.codex (non-breaking change, additive feature)
- Ticket: Add folder migration command — cascade migrate-folder --to [.claude|.codex] — moves CASCADE.md and regenerates symlinks + harness files at new location

### personal — partial

**Exists:**
- Cascade tier system (PCI tier defined at ~/Downloads/.cascade in cascade-types/src/tiers.rs), Memory index aggregation (memoryAggregator.ts across registered project roots), Inbox & Threads viewer (InboxThreadsTab.tsx with cross-project aggregation), Vault navigator (VaultNavigator component + VaultContext), Markdown editor (MarkdownEditor with live preview), Daily notes (openDailyNote service), Templates system (templates.ts service), Wikilinks (wikilinkParser + wikilinkExtension), Backlinks panel (BacklinksPanel component), Graph view (GraphView.tsx with D3), Full-text search (vault_search IPC command), Memory filtering (memoryFilters.ts)
- IPC vault commands (vault_list, vault_read, vault_write, vault_search via commands/vault.rs)
- Settings infrastructure for vault path configuration (cascade-settings.md, VaultProvider props)
- Six-tier taxonomy defined in wiki (Cascade-Resolution.md shows PCI at ~/Downloads/.cascade)

**Gaps:**
- Personal vault (PCI at ~/Downloads/.cascade) NOT initialized or surfaced in UI — vault root is hardcoded to ~/.cascade in VaultContext, App.tsx, and TemplatePicker
- No UI to configure or switch between personal vault (PCI) and global vault (GCI) — VaultProvider.defaultRoot always defaults to ~/.cascade
- Personal vault not populated in onboarding wizard — no guided setup for creating/linking ~/Downloads/.cascade
- Memory index only aggregates project roots (projectPaths from daemon registry), NOT personal vault (.claude/memory/* under PCI)
- Threaded memories stored at {project}/.claude/memory/threads/, but NO personal-level threads at ~/Downloads/.cascade/.claude/memory/threads/
- No UI page to browse/edit personal vault (PCI) separate from global vault — only one vault view exists
- VaultNode tree hardcoded to GCI tiers (GCI/PCI/APC/PPC/PRC/PAC under ~/.cascade/), not reading from actual PCI at ~/Downloads/.cascade
- Personal vault path configurable in settings.json (cascade-settings.md) but NOT wired to VaultContext or UI
- No detection of PCI tier existence or fallback when missing — silent skip like other tiers
- Dashboard 'Personal' panel (E-02) shows threads/ideas/inbox but sources from project .claude/inbox/ only, not from ~/Downloads/.cascade/.claude/inbox/

**Recommended work:**
- T-PERSONAL-01: Expose PCI vault path in AppState — daemon query command vault_get_pci_path returns ~/Downloads/.cascade (or override from settings)
- T-PERSONAL-02: Wire PCI to VaultContext — add optional personalVaultRoot prop to VaultProvider; read from AppState; mount at /vault/personal route
- T-PERSONAL-03: UI tab 'Vaults' in /vault page — Global (GCI at ~/.cascade) vs Personal (PCI at ~/Downloads/.cascade) with switcher
- T-PERSONAL-04: Memory aggregator: include PCI — buildMemoryIndex scans {pci}/.claude/memory/* + {projects}/.claude/memory/* in parallel
- T-PERSONAL-05: Inbox aggregation: include PCI — InboxThreadsTab scans ~/Downloads/.cascade/.claude/inbox/ + {projects}/.claude/inbox/
- T-PERSONAL-06: Threaded memories in PCI — create ~/Downloads/.cascade/.claude/memory/threads/{threadID}/README.md; MemoryViewer lists both project + personal threads
- T-PERSONAL-07: Onboarding step: personal vault — WelcomePhase offers 'Create personal vault at ~/Downloads/.cascade?' with optional init
- T-PERSONAL-08: Dashboard /personal panel: dual-source inbox — merge ~/Downloads/.cascade/.claude/inbox/ entries with ideas/CRD chains
- T-PERSONAL-09: Settings UI: PCI path override — ContextSettings.pciRoot input; daemon resolves override
- T-PERSONAL-10: Daily notes in PCI — useDailyNoteShortcut + openDailyNote support personalVaultRoot; Cmd+Shift+D selects vault (GCI vs PCI) on first run

### unified — mostly

**Exists:**
- cascade-core: ResolvedCascade struct returns merged_instructions (merged_text field) with all 6 tiers merged in priority order (GCI→PCI→APC→PPC→PRC→PAC)
- cascade-cli generate-instructions: per-harness file generator writes CLAUDE.md/AGENTS.md (CC) and opencode-instructions.md (OC) with merged cascade header + MCP server URL reference
- cascade-cli resolve: command outputs merged_text from all discovered tiers, with JSON format supporting tier provenance
- cascade-mcp: 8 MCP tools including cascade.search, cascade.context_slice, cascade.read (per-tier), cascade.inbox, cascade.memory, cascade.master_lists
- cascade-mcp context_slice: token-budgeted context optimizer returning deduplicated context window for harness injection
- cascade-harness policy engine: dispatch-time policy evaluation (simple + WASM) with allow/deny guardrails for bash/read/write/mcp_tool actions
- cascaded daemon: file watcher + resolver + derived-file writer; runs MCP server on Unix socket; indexes RAG database
- cascade.toml per-tier config support for mcp_server_url override (falls back to default unix://~/.cascade/cascade.sock)

**Gaps:**
- No unified data policy per harness: harnesses read per-tier instruction files, not a single merged payload. Each harness independently loads CLAUDE.md/AGENTS.md from multiple tiers and reconciles locally.
- Policy system is harness-agnostic (works for any harness) but NOT automatically enforced per harness: cascade policy eval() is manual CLI call, not hooked into dispatch. Policies are loaded on demand, not pre-applied to harnesses.
- No automatic per-harness policy application: each harness must call cascade dispatch or manually invoke policy.eval() before acting. No 'ONE instruction+policy pair sent to each harness' mechanism.
- MCP instruction delivery incomplete: cascade.read tool reads per-tier files, but there is NO 'cascade.get_merged_instructions_for_harness(harness_name)' tool that returns a single unified payload (instructions + policies) per harness target.
- cascade.context_slice is RAG-based context retrieval, not instruction merging. It returns deduplicated chunks for injection, not the canonical merged instruction set.
- Missing: top-level 'cascade.provide_harness_context(harness=cc|oc|codex) -> {instructions, policies, mcp_config}' tool that harnesses can call on startup to get their complete, merged instruction+policy context in one call.
- No documented harness onboarding flow that ensures ALL harnesses (CC, OC, Codex, Cursor, Aider) receive the SAME merged truth. generate-instructions targets CC/OC; Codex/Cursor/Aider integration stated as future work (T-P4-E06-02..05).
- Policy persistence: policies stored in ~/.cascade/policies/ (toml/wasm) but no integration into cascade.toml per-tier. No way to declare 'this policy applies globally' vs 'only in this project' within the cascade structure itself.

**Recommended work:**
- Epic: Add cascade.provide_harness_context MCP tool — returns {merged_instructions, policies, config} for a named harness (CC|OC|Codex), eliminating per-harness file-reading logic. (Depends on: harness identity in MCP request context)
- Ticket: Integrate policy evaluation into cascade-daemon startup flow so policies are pre-evaluated and cached per project, reducing per-dispatch latency.
- Ticket: Add [policy] section to cascade.toml schema so policies can be declared per-tier with scoping (global|project|repo). Allows 'inherit policy from parent tier' semantics.
- Ticket: Document harness onboarding choreography for CC, OC, Codex, Cursor, Aider showing: (1) cascade init at target tier, (2) cascade generate-instructions --harness X, (3) confirm MCP server reachable, (4) verify ONE merged instruction set is active.
- Ticket: Extend generate-instructions to support --output-single-file flag, writing a UNIFIED harness-specific instruction file (not per-tier) with all tiers merged + policies appended, for harnesses that cannot read MCP.
- Ticket: Add cascade harness init-from-installed subcommand that detects CC/OC/Codex on disk, scaffolds their .cascade/CASCADE.md if missing, and runs generate-instructions + policy linking automatically.
- Ticket: Define cascade.toml [harness] section schema with per-harness config (e.g. 'cascade [[harness]] name=cc MCP_MODE=stdio' to auto-set up harness-specific defaults).
- Epic: Harness Ecosystem & Interop (E-06) — currently in archive (P4); move to active phase with explicit deliverable: 'All AI harnesses (CC, OC, Codex, Cursor, Aider) configured and tested to receive merged cascade in one flow.'

### rag — mostly

**Exists:**
- crates/cascade-rag/src/lib.rs (5KB) — full RAG pipeline with 48 RS files, 3834 LOC
- crates/cascade-rag/src/retrieve/rrf.rs — RRF (Reciprocal Rank Fusion) hybrid merger with FTS5+dense vectors, k=60 configurable
- crates/cascade-rag/src/embed/ (bge_m3, jina, fastembeds) — BGE-M3 ONNX via fastembed-rs, 1024-dim, sparse fallback with TF-IDF
- crates/cascade-rag/src/search.rs — end-to-end search pipeline with SearchConfig tier support
- crates/cascade-rag/src/index_manager.rs — per-project index routing with CASCADE tier awareness, SQLite WAL mode
- crates/cascade-rag/src/ingest.rs (43.8KB) — file→parse→chunk→embed→store pipeline, idempotent via SHA-256, batch embedding (32 chunks)
- crates/cascade-rag/src/chunk/ — markdown, code (tree-sitter), semantic, hierarchical chunkers; max 2000 chars, 200 char overlap by default
- crates/cascade-rag/src/parse/ — DocumentParser + ParseDispatcher for 10 formats (markdown, Rust, TypeScript, Python, PDF, DOCX, XLSX, HTML, YAML, JSON, TOML)
- crates/cascade-rag/src/rerank/ (bge, jina) — bge-reranker-v2-m3 cross-encoder (opt-in, Tier::Reranker+)
- crates/cascade-rag/src/cache/ — QueryCache, EmbedCache, LegacyQueryCache for performance
- crates/cascade-rag/src/citation.rs — Citation struct with file path, line_start/end, chunk_id, RRF score, source hash
- crates/cascade-rag/src/context/mod.rs — ContextOptimizer for token-window budgeting, dedup (SHA-256), shell-output compression

**Gaps:**
- No concrete agent orchestration/routing logic shipped — Agent/AgentPlugin traits defined but no multi-agent dispatch loop or agent team orchestration (cf. vision: 'orchestrates agent teams')
- No chains/workflows orchestration — no task routing, parallel execution coordination, or state machine for multi-step agent work
- Agent naming/namespacing convention not formalized — library YAML has agents/ folder but no defined ID scheme, versioning, capability tags, or team composition patterns
- No on-disk knowledge naming conventions enforced — .cascade/library/ structure exists but no schema for agent composition, knowledge graph linking, or cross-reference validation
- HyDE (Hypothetical Document Embeddings) query expansion stubbed in retrieve/hyde.rs but not wired to live queries — T-P5 deferred
- BGE-M3 sparse vectors (SPLADE-style) use fallback TF-IDF because fastembed 3.14.1 lacks EmbeddingModel::BGEM3 — true M3 sparse output not shipped; upgrade path noted in code comment but not implemented
- MultiVector (ColBERT) mode gated behind rag-multivec feature but retrieval/multivec.rs logic exists and is not fully integrated into RRF pipeline
- No formal agent execution framework — agents can be defined in library but no runtime scheduler, event loop, or tool-call execution layer
- Tool orchestration for agents incomplete — cascade-plugins/src/traits/agent.rs defines tool interface but no centralized tool registry, capability grants, or tool chaining for complex flows
- Memory/knowledge integration with agents not specified — library items reference tools but no memory injection, conversation history persistence, or multi-turn state management for agents
- No Langchain-equivalent chains/runnables abstraction — no composable chain types, connectors, memory backends, or tool binding patterns (e.g., LLMChain, RetrievalQAChain equivalents)

**Recommended work:**
- T-P5-E01: Agent Orchestration Framework — Define multi-agent dispatcher, task queue routing, parallel execution, and team composition patterns. Specify how agents coordinate, share context, and hand off work. Update agent.rs with orchestration traits.
- T-P5-E02: Agent Naming/Versioning Scheme — Formalize agent ID convention (e.g., {domain}-{role}-{version}), capability tagging system, dependency declaration format in library YAML. Update library/types.rs + schema.md.
- T-P5-E03: Knowledge Vault On-Disk Structure — Define folder/file naming for agents, personas, prompts, slash-commands (currently ad-hoc). Enforce with migration + validator. Spec: .cascade/library/{type}/{domain}/{slug}/{version}.yaml or similar.
- T-P5-E04: Chains/Workflows DSL — Design composable workflow types (ChainStep, ChainFlow, RunnableSequence equivalents). Implement minimal async executor for agent chains. Provide YAML spec for defining multi-step agent workflows.
- T-P5-E05: BGE-M3 Sparse Vector Upgrade — Bump fastembed to version supporting EmbeddingModel::BGEM3, remove TF-IDF fallback, ship true SPLADE sparse output in vec_index. Regression test with golden sparse/dense fusion pairs.
- T-P5-E06: Tool Registry & Capability Grants — Centralize tool discovery for agents in cascade-plugins. Implement per-agent capability grant system (read file, search, call LLM, etc.). Bind MCP tools + library tool refs into unified ToolIntegration contract.
- T-P5-E07: Agent Context Persistence — Extend AgentContext with conversation history, memory file injection, cross-session state. Define memory backend interface in cascade-types. Ship default file-based memory store in cascade-core/library/.
- T-P5-E08: Query Expansion (HyDE) — Wire retrieve/hyde.rs into live search path. Implement query expansion dispatcher that opts in per SearchConfig. Regression test vs golden set — HyDE should improve MRR by ≥5% on conceptual queries.
- T-P5-E09: MultiVector ColBERT Integration — Complete multivec.rs implementation, integrate into RRF pipeline as optional 4th channel. Benchmark late-interaction scoring vs baseline. Enable with rag-multivec feature gate + config flag.
- T-P5-E10: Agent Library Schema Validation — Add JSON schema validator to cascade-core/library. Migrate all agent YAML to schema. Add pre-commit hook + cascade doctor check. Ensure agents declare dependencies, tools, memory needs.

### editor — mostly

**Exists:**
- apps/cascade-app/src/features/vault/editor/MarkdownEditor.tsx — CodeMirror 6 editor with markdown syntax highlighting, auto-save, wikilink [[]] support with autocomplete, and Cmd+S manual save
- apps/cascade-app/src/features/vault/graph/GraphView.tsx + graphData.ts — D3 v7 force-directed graph visualization of vault notes, with node sizing by in-degree, directory-based coloring, pan/zoom, and click-to-open
- apps/cascade-app/src/features/vault/daily/TemplatePicker.tsx + useDailyNoteShortcut.ts — daily notes with user-defined template support and Cmd+Shift+D shortcut
- apps/cascade-app/src/components/vault/MemoryViewer.tsx + MemoryCard.tsx — memory browser with 5 tabs (Decisions, Lessons, Patterns, Ideas, Inbox & Threads), search + project filtering
- apps/cascade-app/src/pages/VaultPage.tsx + VaultGraphPage.tsx + MemoryPage.tsx — three dedicated routes for vault editor, graph, and memory views
- apps/cascade-app/src/features/project-map/ProjectMapPanel.tsx — three-tab project visualization (Project Graph, Cascade Tiers, PEWS DAG)
- apps/cascade-app/src/features/project-map/CascadeTierTree.tsx — renders six-tier cascade chain (GCI→PCI→APC→PPC→PRC→PAC) with existence status indicators
- apps/cascade-app/src/types/vault.ts — MemoryEntry type system covering decisions/lessons/patterns/ideas/inbox kinds, extracted from ~.claude/memory and ~.claude/ideas directories
- apps/cascade-app/src/features/vault/editor/wikilinks/wikilinkExtension.ts + wikilinkParser.ts — wikilink [[note-name]] parsing, rendering, and autocomplete integration

**Gaps:**
- No specific instructions-as-a-distinct-object view/browser — CASCADE.md instructions from the six-tier cascade (GCI/PCI/APC/PPC/PRC/PAC) are navigable via CascadeTierTree but not indexed or aggregated like memories; no '/vault/instructions' or equivalent route that surfaces instructions alongside memories
- Instructions and memories are disjoint: MemoryEntry only covers decisions/lessons/patterns/ideas/inbox (from .claude/memory/.claude/ideas/.claude/inbox), while the .cascade/CASCADE.md tier files are treated as structural metadata (for resolver/injection) rather than queryable knowledge objects; no unified instructions+memories+context graph
- Graph view (GraphView.tsx) shows note-connection wikilinks but is explicitly scoped to vault notes only (line 86-95) — does not extend to include instruction dependencies, tier relationships, or cross-project instruction cascades as nodes/edges
- Project map has Cascade Tiers tab showing existence but no deep inspection or diff of tier-specific instructions; no side-by-side comparison of conflicting instructions across tiers or inheritance chain visualization
- No instructions-specific full-text search (vault_search via ripgrep is scoped to .md notes; does not cover CASCADE.md files across tiers)
- Personal scope unclear: MemoryViewer aggregates projectPaths but does not distinguish between APC (All-Projects) memory and Personal-only memory; no Personal-tier scoping or isolation in the UI
- Inbox & Threads tab (InboxThreadsTab.tsx) is a 5th memory kind but threads are read-only; no thread creation/contribution workflow within the editor
- No instructions-to-code traceability: instructions are visible as static markdown, but no links back to implementation files, no rule-violation checkers, no IDE-integration for instruction compliance

**Recommended work:**
- Create /vault/instructions route + InstructionsBrowser component (similar to MemoryViewer) that aggregates and renders CASCADE.md files from all six tiers, with tabs for each tier (GCI/PCI/APC/PPC/PRC/PAC); allow search and selection to jump to the .cascade/ directory or open in editor
- Extend MemoryEntry type + buildMemoryIndex() to optionally include instructions (from CASCADE.md headers/frontmatter) so they appear in a unified 'Knowledge' graph alongside decisions/lessons/patterns; add kind='instruction' to MemoryEntryKind union
- Build InstructionGraphView (optional P4 task): extend GraphView to optionally render instruction-tier nodes and cascade-chain edges, allowing users to visualize how rules flow from GCI→PCI→APC and override patterns
- Add Personal-tier memory filtering: expose VaultContext.projectPaths as a 'Personal' vs. 'All-Projects' toggle in MemoryViewer; read a ~/.cascade/personal_scope.json marker file to separate Personal-only entries from APC entries
- Implement instructions full-text search (ripgrep over all CASCADE.md files in the cascade hierarchy), accessible from SearchPanel or a dedicated 'Instructions' search in CascadeTierTree
- Create instruction editor within CascadeTierTree: allow users to create/edit CASCADE.md files for each tier directly in the UI, with conflict-resolution UI for inherited rules and tier-override markers
- Add instructions-to-memory cross-linking: when creating a Decision/Lesson/Pattern, allow tagging it with the instruction rule it traces back to (e.g., '#rule:error-handling' → links to the GCI error-handling rule)
- P4+: Build instruction-compliance checker — scan codebase against active instructions and flag violations with links to the violated rule and the violation location in code

### kanban — absent

**Exists:**
- Scheduled Tasks (daemon-level execution): crates/cascade-types/src/scheduled_task.rs, ScheduledTaskEntry type in settings, UI in apps/cascade-app/src/features/settings/ScheduledTasksTab.tsx — but this is for cron-based task scheduling, not project task boards
- Project Map visualization: apps/cascade-app/src/features/project-map/ — renders project graphs, PEWS DAG, cascade tier trees, but not task boards
- Memory/Ideas/Inbox subsystem: apps/cascade-app/src/pages/InboxPage.tsx and memory features exist but are placeholders or focused on knowledge vault, not task management

**Gaps:**
- No task/kanban board components built for any scope (global, per-project, or per-team)
- No task data model or schema in cascade-types or cascade-core
- No per-project task lists UI
- No taxonomy/tag system for task categorization (only general tags exist for memory/ideas)
- No task status tracking (backlog/ready/in-progress/review/done)
- No sprint/iteration planning boards
- No cross-project task aggregation view
- No task assignment or ownership tracking
- No task dependency/blocking relationships
- No task search/filtering beyond ideas inbox
- InboxPage exists but is a placeholder stub (line 14 of InboxPage.tsx: just h1 heading)
- No IPC commands for task CRUD operations

**Recommended work:**
- Create P4 epic 'Task Management & Kanban Boards' (or P3.5 if accelerated): (1) define Task data model in cascade-types/src/task.rs with fields: id, title, description, status (Backlog/Ready/Active/Review/Done), project_id, tags[], assignee, priority, created_at, due_date, blockers[], (2) add task store and IPC commands (task_create/task_update/task_list/task_delete) in cascade-daemon, (3) build per-project task board UI in apps/cascade-app/src/features/tasks/ with Kanban-style columns (status-based), (4) add global task aggregation view to Personal panel of browser dashboard, (5) extend taxonomy system to support task tags per the '6-tier Taxonomy' pillar, (6) implement task search and filtering in RAG/RRF (P4)
- Integrate with PEWS phase tracking: ensure tasks can be linked to phase/epic/sprint context for AI-assisted planning
- Define task-to-context injection: allow tasks/tickets to be packed into codebase context for harnesses (e.g., 'active sprint tasks' as context for CC/OC)
- Add scheduled-task execution history UI: extend ScheduledTasksTab to show past runs, logs, failures
- If task boards land before P4, prioritize: (1) per-project board, (2) status columns, (3) drag-drop CRUD, (4) global inbox aggregation

### pews — partial

**Exists:**
- PewsDag.tsx — PEWS DAG visualization in Cascade.app using ReactFlow (P3-E07-13)
- cascade-core::maps::pews_dag module — reads T-*.yaml ticket YAML files under .claude/phases/current/p*/ and builds a dependency graph with status/weight metadata
- Tauri IPC command get_pews_dag (apps/cascade-app/src-tauri/src/commands/maps.rs) — exposes PEWS DAG to frontend
- mapTransforms.ts — pure data transforms converting PEWS GraphData to ReactFlow nodes/edges with topological column layout
- Ticket YAML schema support — parses id, title, status, weight, depends_on fields with inline and block-sequence YAML lists
- MCP cascade://project_state resource — reads phase status from .claude/phases/current/p*/status.yaml to report active phases
- Phase structure in .claude/phases/current/ and .claude/phases/archive/ — hierarchical Phase→Epic→Wave→Sprint→Ticket organization
- MASTER-FEATURES.md entry F-APP-PROJECT-MAP 'Project map graph view — Visual .cascade tier cascade diagram + PEWS phase DAG' (P3-E07, status: Planned)

**Gaps:**
- No native Cascade CLI commands for phase/ticket operations (no eop, eot, eos, eow, cascade phase list/open/close, cascade ticket create/update/close)
- No Cascade CLI API to read active phase, list tickets, update ticket status, mark phases done — all PBD operations are external shell scripts or manual YAML edits
- No MCP tools for phase/ticket management — only read-only resources (cascade://project_state)
- No ticket creation/mutation workflow in Cascade CLI or MCP (T-*.yaml files are authored externally)
- No integration between phase status tracking and Cascade app UI (status.yaml is read but not mutatable from app)
- No SPORT (Single Point of Operational Response) tracking for phase/epic/ticket entities in MASTER-*.md files — PEWS structure is undocumented in SOT
- No dependency management UI — PEWS DAG is read-only visualization; no drag-drop to modify depends_on, no conflict detection
- No ticket bulk operations (mark all in sprint done, cascade status update across wave)
- Phase transitions not automated — moving from P3 to P4, archiving phases, opening new phases requires manual directory manipulation
- Onboarding wizard (P3-E03) plans to collect project info and generate initial phase structure but this is partially shipped and doesn't include interactive phase builder

**Recommended work:**
- E-PBD-01: Cascade CLI phase/ticket commands (epic) — add `cascade phase list/open/close`, `cascade ticket create/list/update/close`, `cascade sprint status`, `cascade eop/eot/eos` equivalents to Rust CLI via CascadeCommand trait. Wire into cascade-core::pbd module
- E-PBD-02: PBD MCP tools (epic) — expand cascade-mcp with tools: read_phase_status, list_tickets, update_ticket_status, batch_close_tickets, create_phase, archive_phase. Pair with MCP resources for ticket YAML read
- E-PBD-03: PEWS mutation UI (epic) — add editable ticket nodes in ReactFlow (P3-E07-13 derivative); on-canvas depends_on link drawing; status dropdown; drag-and-drop reordering. POC with read-only → read-write toggle
- E-PBD-04: SPORT master lists for PBD (ticket) — create MASTER-PHASES.md documenting all active/archived phases, MASTER-TICKETS.md as index of all T-*.yaml with title/status/epic, MASTER-PEWS.md documenting PEWS YAML schema and edge semantics. Link from README.md
- E-PBD-05: Phase transition automation (epic) — add `cascade phase archive <phase-id>`, `cascade phase new <phase-name>`, `cascade eop <phase-id>` that safely: (a) validate all tickets closed/archived, (b) move .claude/phases/current/p*/epics → .claude/phases/archive/p<N>-<date>, (c) create new p<N+1> structure with status.yaml
- E-PBD-06: Ticket creation wizard (epic) — Cascade CLI and app UI flow: `cascade ticket new T-P3-E07-14` with interactive prompts for title, description, weight, owner, depends_on; auto-generates T-*.yaml at correct path in .claude/phases/current/p*/epics/E-XX/waves/W-XX/sprints/S-XX/tickets/
- E-PBD-07: Phase status sync to Cascade app (ticket) — wire status.yaml polling into Cascade.app sidebar or dashboard; show real-time phase progress (X/Y tickets done), active epic, current wave, blocker count. Update on file watcher events
- E-PBD-08: PEWS validation + repair tool (ticket) — linter: check all T-*.yaml files are parseable, depends_on references exist, no cycles, status enum valid. Repair: auto-fix common issues (missing weights, malformed depends_on, orphaned tickets). CLI: `cascade pews validate --repair`

### eie — absent

**Exists:**
- cascade doctor command (crates/cascade-cli/src/cmd/doctor.rs) — environment/config health checks for Cascade's own installation, not user code
- cascade policy command (crates/cascade-cli/src/cmd/policy.rs) — Rego/WASM guardrail policies for controlling harness dispatch operations (secret-file guards, path-traversal, injection guards), not code quality enforcement on user projects
- cascade-core resolver — merges CASCADE.md tiers and writes derived files (CLAUDE.md, AGENTS.md, etc.), for instruction management only
- MCP server (cascade-mcp) — exposes cascade as context to AI tools; extracts & serves user's instructions, not code analysis

**Gaps:**
- No file-size analysis or caps for user code
- No DRY/3NF enforcement (no duplicate detection, no normalization suggestions)
- No code-once-test-reuse enforcement across user projects
- No dependency-order analysis or build validation
- No lint/standards checks user code against conventions
- No metrics/audit of code quality in user projects (code metrics, duplication, complexity)
- No refactoring suggestions based on analysis of user codebase
- No context for code standards that AI agents could leverage (standards are instruction-text only)
- No integration with common linters/formatters/analysis tools (eslint, clippy, mypy, etc.)
- No ability to enforce or suggest adherence to patterns across a user's portfolio

**Recommended work:**
- P5 epic: Code analysis layer — add crate cascade-analyzer with traits for FileMetrics, DuplicateDetector, DependencyResolver, CodeMetricsCollector; expose via cascade search and MCP resources for AI agents to act on
- P5 ticket: User project health dashboard — add cascade health check subcommand to analyze user code (file sizes, cyclical deps, duplication ratios, test coverage) and report summary
- P5 ticket: Pattern enforcement context — teach cascade to extract reusable patterns from user code and suggest them in CASCADE.md (DRY violations, naming conventions, error-handling patterns)
- P5 ticket: Build order validation — integrate with user's build system to verify dependency-ordered builds and export as queryable context
- P5 ticket: MCP code-metrics resources — expose UserCodeMetrics resource type from cascade-mcp so AI agents can see and act on code quality signals
- P5 ticket: Lint policy bridge — allow users to route eslint/clippy/mypy output through cascade policies to gate harness actions based on code quality gates

### agents — minimal

**Exists:**
- Agent trait + Tier enum (cascade-types/src/agent.rs) — T0-T3 capability tiers defined
- AgentPlugin WASM trait (cascade-plugins/src/traits/agent.rs) — autonomous plugins can implement agents
- Agent hook system in cascade-daemon (hook_runner.rs, hook_store.rs) — daemon fires DaemonStart/ProjectLoaded/etc events
- Single-shot harness dispatch (cascade-cli/src/cmd/dispatch.rs) — spawns Claude Code or OpenCode with injected context
- Policy engine (cascade-harness/src/policy/) — evaluates dispatch actions before execution
- MCP tool registry (cascade-mcp/src/tool.rs) — 8 tools including cascade.read, cascade.search, cascade.inbox tools
- Codex harness bridge scaffolding (cascade-harness/src/codex/) — detects Codex, generates AGENTS.md, monitors sessions
- Plugin system with Agent capability (cascade-pdk/src/plugin.rs) — WASM plugins can implement autonomous agents

**Gaps:**
- NO T0 CEO orchestrator — no agent spawning/coordination engine, no multi-agent dispatch loop
- NO agent team framework — no role matrix, no agent registry, no skill/capability matching for task routing
- NO agent-to-agent communication — no inbox/message queue for cross-agent coordination
- NO multi-step reasoning loop — agents are single-invocation stateless; no ReAct-style loop or turn tracking
- NO non-coding automation — no customer-service agents, prompt-driven email, or workflow automation beyond hooks
- NO OpenClaw/Hermes parity — Cascade dispatch is single-shot (Claude/OpenCode), not an orchestrator over a fleet of agents
- NO local LLM dispatch framework — proxy exists (cascade-daemon/src/proxy/) but no multi-agent task queue or model routing
- NO founder/CEO agent integration — no agent that talks to human founder and orchestrates everything beneath
- Agent plugins are DECLARED in PDK but NEVER SPAWNED in daemon or CLI — no call path from daemon to run_agent() on WASM plugins
- Agent context in cascade-plugins/traits/agent.rs is NEVER POPULATED — no conversation history threading, no available_tools injection

**Recommended work:**
- P5 Epic: T0 Agent Orchestrator — build CEO agent (T0 tier) that spawns child agents (T1/T2/T3) via daemon task queue; integrates agent-swarm.md pattern from template context
- P5 Epic: Multi-Agent Dispatch — implement agent registry (cascade-types/agent/registry.rs), task queue (cascade-daemon/src/agent_tasks.rs), and dispatch loop with role-based routing
- P5 Epic: Non-Coding Automation — extend hook system to trigger autonomous agents for customer-service workflows, email drafting, ticket triage (data source agents → LLM → email templates)
- P5 Epic: Agent Communication & Inbox — implement PCI (Project-scoped Cross-agent Inbox) for agent-to-agent messaging, similar to cascade.inbox.* MCP tools but for inter-agent work
- P4 Task: Wire Agent Plugin Lifecycle — add run_agent() call path from cascade-daemon when an Agent plugin is loaded (currently plugins are loaded but agents never invoked)
- P4 Task: Agent Context Population — populate AgentContext.messages, available_tools, session from resolved cascade + MCP registry (currently type exists but is never constructed)
- P4 Task: Founder Integration — designate one T0 agent as 'Founder Orchestrator' in config; expose REST/IPC endpoint for human to send top-level directives
- P4 Task: Local LLM Agent Routing — extend cascade-local-llm/src/ to support agent-workload dispatch (small tasks → Phi3, complex → Llama3, e2e → fallback to API provider)

### promptbox — mostly

**Exists:**
- Browser dashboard GP Chat panel at apps/cascade-dashboard (fully working with SSE streaming, 10-tool catalog: list_projects, list_tasks, read_file, read_memory, read_inbox, read_cache_quota, read_phase_status, write_idea, send_pci, open_dashboard_tab)
- POST /api/chat SSE handler in crates/cascade-daemon/src/http/chat_handlers.rs proxying to Gemini pool at localhost:3761 with streaming token + tool_result events
- Gemini Free Pool (GFP) key registration + rotation in crates/cascade-daemon/src/ipc_pool_register.rs (gemini-keys.json hot-reload)
- MCP sampling handler (sampling/createMessage) in crates/cascade-mcp/src/sampling.rs allowing server-initiated LLM calls via ProviderRegistry
- MCP prompts primitives (prompts/list, prompts/get) in crates/cascade-mcp/src/handlers/prompts.rs exposing 3 built-in cascade context injection templates
- ProviderAdapter trait + ProviderRegistry in crates/cascade-providers for unified provider dispatch
- LocalLlmAdapter (cascade-local-llm crate) supporting gemma-2-2b, llama-3.2-3b, phi-3-mini with candle-rs + Metal acceleration on macOS
- MCP server with 5 transports exposed from cascaded daemon (stdio, Unix socket, SSE, HTTP, websocket)

**Gaps:**
- Desktop Tauri app (cascade-app) has NO integrated prompt box / chat UI — DashboardPage is a placeholder; users must open localhost:9761 dashboard in separate browser window instead of integrated desktop chat panel per vision
- Tauri app does not wire POST /api/chat endpoint or GPChatPanel component — no desktop-native chat streaming
- Local-llm provider adapter is NOT registered in the daemon's ProviderRegistry at startup; only Gemini pool proxy is wired in chat_handlers.rs (line 37: hardcoded GEMINI_PROXY_URL)
- No added_subs (additional subscriptions / merged context) integration in prompt box; GP Chat tool catalog is static 10-tool set, does not access daemon's added_subs / context optimization layer
- MCP tools (cascade-mcp) are designed for external MCP clients (Claude Desktop, Claude Code) — NOT integrated into desktop Tauri app as in-app tools
- No UI for subscribing / selecting context sources (local-llm vs GFP vs provider routing) — hardcoded to Gemini pool only
- Chat history persistence not integrated with Tauri app; GPChatPanel uses localStorage only (no daemon-backed persistence option implemented)
- Provider routing table (RoutingTable, round-robin + cooldown) is implemented but NOT exposed to prompt box — no visible provider selection / fallback UI in chat panel
- No local-llm inference streaming path wired; if Gemini pool is down, users have no fallback to local model inference

**Recommended work:**
- E-06-24: Integrate GP Chat panel into Tauri app as a floating window or sidebar; port cascade-dashboard/src/components/GPChatPanel/ into cascade-app with Tauri invoke commands wired to daemon /api/chat (M ticket, depends on E-06 main app shell)
- E-06-25: Wire provider routing + local-llm fallback in chat handler; modify chat_handlers.rs to consult ProviderRegistry.default_for_task(TaskType::Chat) instead of hardcoded Gemini proxy, support local-llm inference streaming (M ticket)
- E-06-26: Implement provider selection UI in chat panel — dropdown to choose primary provider (Gemini, Local LLM, etc.), visualize active provider + fallback status, persist user preference (S ticket, depends on E-06-24)
- E-06-27: Expose added_subs context injection in GP Chat system prompt; modify Tauri chat endpoint to prepend cascaded context from daemon (read daemon state + merged instruction cascade) before forwarding to LLM (M ticket)
- E-06-28: Wire MCP tools catalog into Tauri chat endpoint; allow Tauri in-app chat to invoke MCP tools via cascade-mcp/src/tool.rs dispatch (not just browser dashboard), expose tool results in UI (L ticket)
- E-07-02: Add local-llm model download + setup flow to onboarding wizard; detect system resources, offer phi-3-mini or llama-3.2-3b as GFP alternative, manage ~/.cascade/models/ (M ticket, depends on E-03 wizard foundation)
- E-02-24: Create .github/wiki/cascade-dashboard.md documenting GP Chat architecture, tool catalog, provider routing, streaming protocol (S documentation ticket, part of E-02 DoD residue)

### ccproxy — absent

**Exists:**
- crates/cascade-daemon/src/harness_bridge.rs — HarnessType enum (ClaudeCode, OpenCode), detect() for binary presence, invoke() with `-p` flag (lines 1-100+)
- crates/cascade-cli/src/cmd/dispatch.rs — dispatch command that composes prompts and invokes CC via `claude -p '<prompt>'` (lines 1-150+)
- crates/cascade-cli/src/proxy_client.rs — Gemini-only proxy token auth for localhost:3761 (T-P2-E07-01), NOT Claude Code
- crates/cascade-daemon/src/proxy/mod.rs + gemini_proxy.rs — HTTP proxy ONLY for Gemini key rotation, no CC involvement (T-P2-E02-36)
- crates/cascade-types/src/harness.rs — HarnessType enum + CodexInfo, OpenCodeInfo types
- P4-E06 epic (Harness Ecosystem & Interop) — scopes Codex bridge + MCP registry + policy engine, explicitly excludes CC API proxy (out of scope)

**Gaps:**
- NO proxy server that API-izes the interactive `claude -p` invocation — CC cannot be called as a service/API; still CLI-only
- NO subscription/access-token management for CC credentials — harness_bridge invokes via CLI, no auth token injection or session management
- NO HTTP/gRPC endpoint that wraps CC prompt dispatch — integrations must shell out to `claude -p` manually
- NO request/response marshaling for CC — no JSON-RPC bridge, no stdout capture standardization, no streaming response API
- NO multi-session management or rate-limiting for CC invocations — each dispatch is standalone, no pool or quota tracking
- NO explicit 'optional beta' feature flag in codebase — V1 design treats this as out-of-scope, not marked as deferred/planned

**Recommended work:**
- **Epic: Claude Code API Bridge (P5+)** — scope a new crate `cascade-ccapi/` to wrap the daemon proxy pattern (gemini_proxy.rs) for CC: HTTP POST /api/cc/dispatch accepting {prompt, repo, timeout_secs}, returning {stdout, stderr, exit_code, duration_ms}
- **Auth token management (prerequisite)** — extend cascade-providers ProviderAdapter pattern to support CC credentials (read ~/.claude/settings.json, detect API key or session cookie, inject as env var to claude -p subprocess)
- **Request/response streaming** — design Server-Sent Events (SSE) endpoint /api/cc/dispatch-stream that yields line-delimited JSON {type: 'stdout'|'stderr'|'exit', data: ...} for long-running prompts
- **Multi-harness pooling & quota (P5+)** — extend cascade-daemon event bus to track per-harness invocation counts, implement token-bucket rate limiter for CC dispatch, expose quota.usage via /api/quota endpoint
- **CLI integration** — add `cascade proxy start --harness cc [--port 3761]` to boot the CC proxy server alongside Gemini proxy; document as 'OPTIONAL BETA: experimental CC API endpoint'
- **Feature flag in settings.json** — add `[experimental]` section with `cc_api_enabled: bool` (default false); document v0.1.0 limitations (no session pooling, no streaming yet)
- **SPORT documentation** — add MASTER-CCAPI.md entry; update Architecture.md to show CC API endpoint alongside Gemini proxy; mark as T-P5-E?-?? tickets
- **OSS option audit (prerequisite)** — research alternatives: (a) claude-code-server (VSCode bridge), (b) Anthropic API direct (not interactive), (c) thin wrapper that muxes socket connections — decide which aligns with 'optional BETA'
- **Integration tests** — T-P5-E?-??: dispatch 'What is 2+2?' via HTTP, verify CC stdout captured; test timeout + process cleanup; verify auth failure returns 401
