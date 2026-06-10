---
id = "pac-default"
version = "1.0.0"
tier = "pac"
stacks = []
project_shapes = []
description = "Vendor-neutral default instructions for the Per-App Cascade tier."
---

# Per-App Cascade Instructions (PAC)

This file applies to the app at `{{APP_DIR}}` inside the repo. It inherits
from GCI, APC, PPC, and PRC. Rules here are specific to this app subdirectory.

Use PAC files when a single repo contains multiple distinct apps with different
frameworks, entry points, or conventions. If the repo has only one app,
consolidate everything into the PRC file instead.

## App Identity

Name: {{APP_NAME}}
Path: `{{APP_DIR}}`
App type: {{APP_TYPE}} (e.g. web frontend, desktop, mobile, CLI, library)
Entry point: `{{ENTRY_POINT}}` (e.g. `src/main.tsx`, `src/main.rs`)

## Framework and Version

Framework: {{FRAMEWORK}}
Version: {{FRAMEWORK_VERSION}}
Minimum runtime version: {{RUNTIME_VERSION}}

Any framework upgrade must be reviewed before it is applied. Check the
framework's migration guide and update this field after the upgrade lands.

## Component Patterns

Structure new components following this pattern:

```
{{COMPONENT_DIR}}/
  {{ComponentName}}.{{EXT}}     # Component implementation
  {{ComponentName}}.test.{{EXT}} # Co-located test
  index.{{EXT}}                 # Re-export (optional, when needed)
```

Rules:
- One component per file.
- Props/types defined in the same file or in a co-located `types.{{EXT}}`.
- No business logic in presentational components — extract to a hook or
  service layer.
- Shared components go in `{{SHARED_COMPONENT_DIR}}`.

## State Management

Approach: {{STATE_APPROACH}} (e.g. local state only, context, external store)
Store location: `{{STORE_PATH}}` (if using an external store)

Rules:
- Prefer local state for UI-only state (open/closed, input values).
- Lift state to a shared store only when two or more unrelated components
  need the same data.
- Do not put server-response data in a client store unless caching is required.

## Routing

Router: {{ROUTER}} (e.g. file-based, manual, none)
Routes file: `{{ROUTES_FILE}}`
Base path: `{{BASE_PATH}}`

Document new routes in the routes file before implementing the component.

## Build and Dev

Dev server command: `{{DEV_COMMAND}}`
Build command: `{{BUILD_COMMAND}}`
Output directory: `{{BUILD_OUTPUT_DIR}}`
Environment variables: defined in `{{ENV_FILE}}`

Do not commit secrets or local env overrides. The `{{ENV_FILE}}` should
contain only example keys with empty values.

## Assets and Styles

Assets: `{{ASSETS_DIR}}`
Styles: {{STYLE_APPROACH}} (e.g. co-located CSS modules, global stylesheet, utility classes)
Design tokens / theme: `{{THEME_FILE}}`
