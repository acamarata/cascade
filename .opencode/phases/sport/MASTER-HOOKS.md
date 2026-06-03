# MASTER-HOOKS.md — Cascade Dashboard React Hooks

**Purpose:** Registry of every custom React hook in the Cascade dashboard.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P3 plan

| Hook | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| useDaemon | src/hooks/useDaemon.ts | Daemon status polling, connect/disconnect | 🔲 Planned | P2/P3 | T-P2-E04-*, T-P3-E01-* |
| useDaemonStatus | src/hooks/useDaemonStatus.ts | Live daemon health/status subscription | 🔲 Planned | P3 | T-P3-E01-* |
| useProviders | src/hooks/useProviders.ts | AI provider list + status, add/remove/test | 🔲 Planned | P3 | T-P3-E05-* |
| useProjects | src/hooks/useProjects.ts | ~/Sites project enumeration + phase state | 🔲 Planned | P3 | T-P3-E02-* |
| useRepos | src/hooks/useRepos.ts | Repo list within a project, PRI/PAI cascade | 🔲 Planned | P3 | T-P3-E02-* |
| useScaffold | src/hooks/useScaffold.ts | PEWS scaffold actions for a project | 🔲 Planned | P3 | T-P3-E02-* |
| usePersonal | src/hooks/usePersonal.ts | Personal panel data: threads, ideas, inbox, CRD | 🔲 Planned | P3 | T-P3-E02-* |
| useGCI | src/hooks/useGCI.ts | GCI/global panel data: rules, refs, hooks, memory, skills | 🔲 Planned | P3 | T-P3-E02-* |
| useUsageHistory | src/hooks/useUsageHistory.ts | Token/cost usage history by account | 🔲 Planned | P3 | T-P3-E02-* |
| useChat | src/hooks/useChat.ts | GP chat messages, SSE stream, send | 🔲 Planned | P3 | T-P3-E02-* |
| useChatHistory | src/hooks/useChatHistory.ts | Persisted chat history per session | 🔲 Planned | P3 | T-P3-E02-* |
| useCommandPalette | src/hooks/useCommandPalette.ts | Cmd+K palette open/close, query, results | 🔲 Planned | P3 | T-P3-E01-* |
| useCommandRegistry | src/hooks/useCommandRegistry.ts | Register/unregister palette commands | 🔲 Planned | P3 | T-P3-E01-* |
| useKeyboard | src/hooks/useKeyboard.ts | Global keyboard shortcut bindings | 🔲 Planned | P3 | T-P3-E01-* |
| useArrowNav | src/hooks/useArrowNav.ts | Arrow-key navigation within list components | 🔲 Planned | P3 | T-P3-E01-* |
| useTheme | src/hooks/useTheme.ts | Theme (light/dark/system) state + toggle | 🔲 Planned | P3 | T-P3-E01-* |
| useWindowState | src/hooks/useWindowState.ts | Tauri window state: size, position, focus | 🔲 Planned | P3 | T-P3-E01-* |
| useWizard | src/hooks/useWizard.ts | Onboarding wizard step state machine | 🔲 Planned | P3 | T-P3-E03-* |
| useWizardLaunch | src/hooks/useWizardLaunch.ts | Trigger wizard launch on first run | 🔲 Planned | P3 | T-P3-E03-* |
| useCheckpoint | src/hooks/useCheckpoint.ts | Wizard checkpoint save/restore | 🔲 Planned | P3 | T-P3-E03-* |
