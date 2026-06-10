# MASTER-ROUTES.md — Cascade Frontend Routes

**Purpose:** Registry of every frontend route in the Cascade dashboard (Vite SPA).
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P3 plan

| Route | Component | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| / | redirect → /dashboard | Root redirect | 🔲 Planned | P3 | T-P3-E01-* |
| /dashboard | DashboardPage | Main dashboard landing (status overview) | 🔲 Planned | P3 | T-P3-E01-* |
| /projects | ProjectsPage | ~/Sites projects list + phase state | 🔲 Planned | P3 | T-P3-E02-* |
| /personal | PersonalPage | Threads, ideas, inbox, CRD, usage | 🔲 Planned | P3 | T-P3-E02-* |
| /global | GlobalPage | GCI rules, references, hooks, memory, RAG | 🔲 Planned | P3 | T-P3-E02-* |
| /settings | SettingsPage | Settings snapshot, theme, update channel | 🔲 Planned | P3 | T-P3-E01-* |
| /onboarding | OnboardingPage | Initial setup wizard (redirected on first run) | 🔲 Planned | P3 | T-P3-E03-* |
| /project-map | ProjectMapPage | Three-tab map panel: project graph, cascade tier tree, PEWS DAG | ✅ Done | P3 | T-P3-E07-13 |
