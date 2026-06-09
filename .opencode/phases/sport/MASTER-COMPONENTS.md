# MASTER-COMPONENTS.md — Cascade Dashboard React Components

**Purpose:** Registry of every React component in the Cascade dashboard (apps/cascade-app).
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09
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
| RulesPanel | src/components/global/RulesPanel.tsx | GCI rules list with search | 🔲 Planned | P3 | T-P3-E02-* |
| ReferencesPanel | src/components/global/ReferencesPanel.tsx | GCI references browser | 🔲 Planned | P3 | T-P3-E02-* |
| HooksEditor | src/components/global/HooksEditor.tsx | CC hooks list/add/edit/delete | 🔲 Planned | P3 | T-P3-E02-* |
| SettingsSnapshotPanel | src/components/global/SettingsSnapshotPanel.tsx | JSON tree of settings.json (redacted) | 🔲 Planned | P3 | T-P3-E02-* |
| RAGStatusPanel | src/components/global/RAGStatusPanel.tsx | RAG index status + stats | 🔲 Planned | P3/P4 | T-P3-E06-*, T-P4-E02-* |
| GPChatButton | src/components/GPChatPanel/GPChatButton.tsx | Floating always-visible chat trigger button | ✅ Done | P3 | T-P3-E02-21 |
| GPChatPanel | src/components/GPChatPanel/GPChatPanel.tsx | Expandable/resizable/minimizable GP chat panel | ✅ Done | P3 | T-P3-E02-21 |
| MessageList | src/components/GPChatPanel/MessageList.tsx | User/assistant messages with streaming cursor | ✅ Done | P3 | T-P3-E02-21 |
| ChatInput | src/components/GPChatPanel/ChatInput.tsx | Textarea + send button for chat | ✅ Done | P3 | T-P3-E02-21 |
| MarkdownMessage | src/components/GPChatPanel/MarkdownMessage.tsx | react-markdown + syntax highlighting (replaces CodeBlock) | ✅ Done | P3 | T-P3-E02-22 |
| ToolCard | src/components/GPChatPanel/ToolCard.tsx | Tool-invocation result card in chat | ✅ Done | P3 | T-P3-E02-22 |
| CommandPalette | src/components/CommandPalette.tsx | Cmd+K command palette with fuzzy search | 🔲 Planned | P3 | T-P3-E01-* |
| OnboardingWizard | src/components/onboarding/OnboardingWizard.tsx | Multi-step onboarding flow with checkpoint resume | 🔲 Planned | P3 | T-P3-E03-* |
