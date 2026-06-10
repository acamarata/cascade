# MASTER-COMPONENTS.md — Cascade Dashboard React Components

**Purpose:** Registry of every React component in the Cascade dashboard (apps/cascade-app).
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09 (E-04 T-P3-E04-25 WizardProviderStep)
**Source:** Cascade P3/P4 plan

| Component | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| AppShell | src/components/layout/AppShell.tsx | Root layout: sidebar nav, main content area, theme | 🔲 Planned | P3 | T-P3-E01-* |
| Sidebar | src/components/layout/Sidebar.tsx | Left nav: route links, active state, collapse | 🔲 Planned | P3 | T-P3-E01-* |
| ProviderCard | src/components/providers/ProviderCard.tsx | AI provider status card: name, status badge, actions | 🔲 Planned | P3 | T-P3-E05-* |
| ProviderStatusBadge | src/components/providers/ProviderStatusBadge.tsx | Color-coded provider health badge | 🔲 Planned | P3 | T-P3-E05-* |
| ProviderActionsMenu | src/components/providers/ProviderActionsMenu.tsx | Dropdown: test, add key, remove provider | 🔲 Planned | P3 | T-P3-E05-* |
| DiffPanel | src/components/merge-engine/DiffPanel.tsx | Side-by-side diff: legacy source vs proposed cascade section | 🔲 Planned | P3 | T-P3-E04-* |
| WizardLayout | src/components/onboarding/WizardLayout.tsx | Onboarding wizard shell: numbered step sidebar + content | 🔲 Planned | P3 | T-P3-E03-* |
| WizardStepper | src/components/onboarding/WizardStepper.tsx | Step indicator: name, completion checkmark, current | 🔲 Planned | P3 | T-P3-E03-* |
| ASIOverviewCard | src/components/projects/ASIOverviewCard.tsx | Project-level ASI summary card | 🔲 Planned | P3 | T-P3-E02-* |
| ProjectCard | src/components/projects/ProjectCard.tsx | Expandable project card with repo list | 🔲 Planned | P3 | T-P3-E02-* |
| ThreadsPanel | src/components/personal/ThreadsPanel.tsx | Personal threads list with status | 🔲 Planned | P3 | T-P3-E02-* |
| IdeasPanel | src/components/personal/IdeasPanel.tsx | Personal ideas browser | 🔲 Planned | P3 | T-P3-E02-* |
| InboxPanel | src/components/personal/InboxPanel.tsx | PCI inbox messages list | 🔲 Planned | P3 | T-P3-E02-* |
| UsageHistoryChart | src/components/personal/UsageHistoryChart.tsx | Weekly usage bar chart (Recharts) | 🔲 Planned | P3 | T-P3-E02-* |
| WeeklyUsageChart | apps/cascade-dashboard/src/components/Analytics/WeeklyUsageChart.tsx | Weekly usage bar chart (Recharts) | ✅ Done | P3 | T-P3-E02-24/25 |
| UsageSummaryRow | src/components/Analytics/UsageSummaryRow.tsx | Per-account usage summary row | ✅ Done | P3 | T-P3-E02-24 |
| AccountLedger | src/components/Analytics/AccountLedger.tsx | Account ledger table with totals | ✅ Done | P3 | T-P3-E02-25 |
| RulesPanel | src/components/global/RulesPanel.tsx | GCI rules list with search | 🔲 Planned | P3 | T-P3-E02-* |
| ReferencesPanel | src/components/global/ReferencesPanel.tsx | GCI references browser | 🔲 Planned | P3 | T-P3-E02-* |
| HooksEditor | src/components/global/HooksEditor.tsx | CC hooks list/add/edit/delete | 🔲 Planned | P3 | T-P3-E02-* |
| HooksList | src/components/HooksEditor/HooksList.tsx | Rendered list of CC hooks | ✅ Done | P3 | T-P3-E02-27 |
| HookForm | src/components/HooksEditor/HookForm.tsx | Add/edit hook form | ✅ Done | P3 | T-P3-E02-27 |
| DeleteConfirm | src/components/HooksEditor/DeleteConfirm.tsx | Hook delete confirmation dialog | ✅ Done | P3 | T-P3-E02-27 |
| SettingsSnapshotPanel | src/components/global/SettingsSnapshotPanel.tsx | JSON tree of settings.json (redacted) | 🔲 Planned | P3 | T-P3-E02-* |
| RAGStatusPanel | src/components/global/RAGStatusPanel.tsx | RAG index status + stats | 🔲 Planned | P3/P4 | T-P3-E06-*, T-P4-E02-* |
| HarnessPanel | src/components/HarnessPanel/HarnessPanel.tsx | Harness integration status panel | ✅ Done | P3 | T-P3-E02-28 |
| RagStatusCard | src/components/HarnessPanel/RagStatusCard.tsx | RAG/RRF status card (P3 stub) | ✅ Done | P3 | T-P3-E02-29 |
| useUsageHistory | src/hooks/useUsageHistory.ts | Hook: fetch historical usage aggregates | ✅ Done | P3 | T-P3-E02-24 |
| GPChatButton | src/components/GPChatPanel/GPChatButton.tsx | Floating always-visible chat trigger button | ✅ Done | P3 | T-P3-E02-21 |
| GPChatPanel | src/components/GPChatPanel/GPChatPanel.tsx | Expandable/resizable/minimizable GP chat panel | ✅ Done | P3 | T-P3-E02-21 |
| MessageList | src/components/GPChatPanel/MessageList.tsx | User/assistant messages with streaming cursor | ✅ Done | P3 | T-P3-E02-21 |
| ChatInput | src/components/GPChatPanel/ChatInput.tsx | Textarea + send button for chat | ✅ Done | P3 | T-P3-E02-21 |
| MarkdownMessage | src/components/GPChatPanel/MarkdownMessage.tsx | react-markdown + syntax highlighting (replaces CodeBlock) | ✅ Done | P3 | T-P3-E02-22 |
| ToolCard | src/components/GPChatPanel/ToolCard.tsx | Tool-invocation result card in chat | ✅ Done | P3 | T-P3-E02-22 |
| CommandPalette | src/components/CommandPalette.tsx | Cmd+K command palette with fuzzy search | 🔲 Planned | P3 | T-P3-E01-* |
| WizardRouter | apps/cascade-app/src/components/onboarding/WizardRouter.tsx | State-machine router for 10-phase wizard; reads checkpoint, dispatches to phase components | ✅ Done | P3 | T-P3-E03-01..08 |
| WelcomePhase | apps/cascade-app/src/components/onboarding/phases/Welcome.tsx | Step 0: intro screen + start/resume | ✅ Done | P3 | T-P3-E03-01 |
| ProviderConnectPhase | apps/cascade-app/src/components/onboarding/phases/ProviderConnect.tsx | Step 1: AI provider connection (Gemini pool + auto-auth) | ✅ Done | P3 | T-P3-E03-08,43 |
| ScanLegacyPhase | apps/cascade-app/src/components/onboarding/phases/ScanLegacyPhase.tsx | Step 2: scan ~/.claude and ~/Sites for legacy configs | ✅ Done | P3 | T-P3-E03-14..16 |
| MergeContentPhase | apps/cascade-app/src/components/onboarding/phases/MergeContentPhase.tsx | Step 3: AI-assisted merge of legacy content into cascade format | ✅ Done | P3 | T-P3-E03-17..20 |
| ToolModesPhase | apps/cascade-app/src/components/onboarding/phases/ToolModesPhase.tsx | Step 4: configure tool modes (symlink vs archive) | ✅ Done | P3 | T-P3-E03-21..22 |
| VerifyDiffPhase | apps/cascade-app/src/components/onboarding/phases/VerifyDiffPhase.tsx | Step 5: review diff of proposed changes before apply | ✅ Done | P3 | T-P3-E03-23 |
| ArchiveLegacyPhase | apps/cascade-app/src/components/onboarding/phases/ArchiveLegacyPhase.tsx | Step 6: archive legacy tools (preflight + execute) | ✅ Done | P3 | T-P3-E03-24..26 |
| SymlinkSetupPhase | apps/cascade-app/src/components/onboarding/phases/SymlinkSetupPhase.tsx | Step 7: create ~/.cascade symlinks for harness tools | ✅ Done | P3 | T-P3-E03-27..28 |
| DaemonInstallPhase | apps/cascade-app/src/components/onboarding/phases/DaemonInstallPhase.tsx | Step 8: install + start cascaded daemon | ✅ Done | P3 | T-P3-E03-29..30 |
| DonePhase | apps/cascade-app/src/components/onboarding/phases/DonePhase.tsx | Step 9: completion screen + TutorialOverlay trigger | ✅ Done | P3 | T-P3-E03-31 |
| TutorialOverlay | apps/cascade-app/src/components/onboarding/TutorialOverlay.tsx | Post-wizard feature tour overlay | ✅ Done | P3 | T-P3-E03-32 |
| DiffPanel | apps/cascade-app/src/components/merge-engine/DiffPanel.tsx | Side-by-side diff: legacy source vs proposed cascade section | ✅ Done | P3 | T-P3-E03-19 |
| SectionTabs | apps/cascade-app/src/components/merge-engine/SectionTabs.tsx | Tab nav for multi-section merge review | ✅ Done | P3 | T-P3-E03-19 |
| SourceFileList | apps/cascade-app/src/components/merge-engine/SourceFileList.tsx | Scanned legacy source files list | ✅ Done | P3 | T-P3-E03-17 |
| ProposedContent | apps/cascade-app/src/components/merge-engine/ProposedContent.tsx | Editable proposed cascade content for each section | ✅ Done | P3 | T-P3-E03-18 |
| RerunMergeDialog | apps/cascade-app/src/components/merge-engine/RerunMergeDialog.tsx | Confirm dialog to re-run AI merge on a section | ✅ Done | P3 | T-P3-E03-20 |
| GeminiPoolStep | apps/cascade-app/src/components/onboarding/steps/GeminiPoolStep.tsx | Sub-step: detect + register Gemini pool keys | ✅ Done | P3 | T-P3-E03-04..06 |
| AutoAuthStep | apps/cascade-app/src/components/onboarding/steps/AutoAuthStep.tsx | Sub-step: scan + import auto-auth tokens | ✅ Done | P3 | T-P3-E03-07 |
| ProvisionCard | apps/cascade-app/src/components/wizard/ProvisionCard.tsx | OAuth provision progress card (start/poll/cancel) | ✅ Done | P3 | T-P3-E03-08 |
| AIGatedStep | apps/cascade-app/src/components/wizard/AIGatedStep.tsx | Wrapper that gates step execution behind a connected AI provider | ✅ Done | P3 | T-P3-E03-08 |
| RestoreToolSection | apps/cascade-app/src/components/settings/RestoreToolSection.tsx | Settings: restore an archived legacy tool | ✅ Done | P3 | T-P3-E03-33 |
| ToolModeSection | apps/cascade-app/src/components/settings/ToolModeSection.tsx | Settings: change tool mode (symlink/archive) with dialogs | ✅ Done | P3 | T-P3-E03-33 |
| useCheckpoint | apps/cascade-app/src/hooks/useCheckpoint.ts | Hook: save/load/clear wizard checkpoint via Tauri commands | ✅ Done | P3 | T-P3-E03-02 |
| useProviderConnected | apps/cascade-app/src/hooks/useProviderConnected.ts | Hook: poll provider health until connected | ✅ Done | P3 | T-P3-E03-08 |
| OnboardingWizard | src/components/onboarding/OnboardingWizard.tsx | Multi-step onboarding flow with checkpoint resume | ✅ Done | P3 | T-P3-E03-01..43 |
| wizard E2E test suite | e2e/wizard.integration.test.tsx | Full-flow integration test (20 tests, all 10 wizard phases, all commands mocked) | ✅ Done | P3 | T-P3-E03-34 |
| wizard WDIO E2E scaffold | e2e/wizard.e2e.ts + e2e/wdio.conf.ts | WebdriverIO spec for Tauri binary E2E (@ci-with-display, scaffolded) | ✅ Done | P3 | T-P3-E03-34 |
| tauriMocks | e2e/mocks/tauriMocks.ts | Mock layer for @tauri-apps/api/core invoke (all wizard commands) | ✅ Done | P3 | T-P3-E03-34 |
| wizard.helpers | e2e/helpers/wizard.helpers.ts | renderWizard() factory + step assertion helpers | ✅ Done | P3 | T-P3-E03-34 |
| WizardProviderStep | apps/cascade-app/src/components/wizard/WizardProviderStep.tsx | Step 2 real provider-connect: 4 quick-connect cards, GFP auto-detect banner, skip-for-now alert, cascade_providers_list gate | ✅ Done | P3 | T-P3-E04-25 |
| TemplatePickerCompact | apps/cascade-app/src/components/template/TemplatePickerCompact.tsx | Compact multi-select template picker for wizard Phase 4 pre-scaffold; search + tier/stack chips + scrollable card list | ✅ Done | P3 | T-P3-E05-19 |
