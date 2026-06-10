# Templates

Cascade ships with 33 bundled templates that populate your `CASCADE.md` files with sensible, opinionated defaults. Each template targets a specific tier, stack, or project shape. You can apply multiple templates to the same file, author your own, and share them.

---

## What templates are

A template is a Markdown file with a TOML frontmatter block. The frontmatter declares metadata. The body supplies `##`-headed sections that get merged into your `CASCADE.md`.

Templates compose along three dimensions:

| Dimension | What it controls |
|---|---|
| **Tier** | Which cascade level the template targets (GCI, PCI, APC, PPC, PRC, PAC, or `any`). |
| **Stack** | Technology tags (e.g. `rust`, `tauri`, `react-vite`). Empty means all stacks. |
| **Shape** | Project-shape tags (e.g. `cli-tool`, `saas-product`, `open-source-library`). Empty means all shapes. |

You can apply a tier template for baseline rules, then stack or shape templates for specifics. Sections never duplicate — each `##` heading appears once.

---

## Template catalog

### Tier templates

These set the defaults for a given cascade tier. Apply one per tier.

| ID | Tier | Description |
|---|---|---|
| `gci-default` | gci | Vendor-neutral defaults for the Global Cascade tier. |
| `pci-default` | pci | Vendor-neutral defaults for the Personal Cascade Instructions tier. |
| `apc-default` | apc | Vendor-neutral defaults for the All-Projects Cascade tier. |
| `ppc-default` | ppc | Vendor-neutral defaults for the Per-Project Cascade tier. |
| `prc-default` | prc | Vendor-neutral defaults for the Per-Repo Cascade tier. |
| `pac-default` | pac | Vendor-neutral defaults for the Per-App Cascade tier. |

### Stack templates

Apply a stack template alongside a tier template when you use a specific technology.

| ID | Stacks | Description |
|---|---|---|
| `astro` | astro | Astro 4+ conventions: islands, MDX, TypeScript, content collections, build tooling, and common pitfalls. |
| `django` | python, django | Django 5.x + DRF service: pytest-django, Black, isort, Alembic-style migrations. |
| `flutter` | flutter | Flutter + Dart conventions: BLoC architecture, file layout, testing, and common pitfalls. |
| `go-module` | go | Go module layout with golangci-lint and testify — Go 1.22+. |
| `kotlin-app` | kotlin, android, gradle | Kotlin 2.x Android/JVM app: Gradle, JUnit 5, ktlint. |
| `nextjs` | nextjs | Next.js App Router conventions: layout, testing, build tooling, lint, and common pitfalls. |
| `node-express` | node, express, typescript | Node/Express service: TypeScript, Vitest, ESLint/Prettier. |
| `python-fastapi` | python, fastapi | FastAPI service: Pydantic v2, Alembic, pytest-asyncio, uvicorn. |
| `python-lib` | python | Python library: src layout, pyproject.toml, pytest, ruff, mypy strict. |
| `react-native` | react-native | React Native with Expo SDK: file layout, EAS, testing, and common pitfalls. |
| `react-vite` | react-vite | React 18 + Vite 5 SPA conventions: file layout, testing, build tooling, shadcn/ui, lint. |
| `rust-binary` | rust-binary | Rust binary crate conventions: Clap v4, tracing, Tokio async runtime, file layout. |
| `rust-crate` | rust-crate | Rust library crate conventions: doc-tests, public API design, no_std compatibility. |
| `swift-app` | swift, swiftui, ios, macos | Swift 5.10+ SwiftUI app: ViewModel/View split, XCTest, SwiftLint. |
| `tauri-2` | tauri-2 | Tauri 2 conventions: Rust backend, React+Vite frontend, IPC patterns, capability model. |
| `unity-game` | unity, csharp | Unity 6+ game project: C#, Assembly Definitions, NUnit, Scene/Prefab conventions. |

### Shape templates

Shape templates capture norms that span multiple technologies but apply to a specific kind of project.

| ID | Shape | Description |
|---|---|---|
| `acamarata-packages` | acamarata-packages | Conventions used by the acamarata org for its public npm and pub.dev packages. |
| `cli-tool` | cli-tool | A CLI tool distributed as a binary or script, invoked in a terminal. |
| `data-science` | data-science | Data analysis, ML, or research workflows where reproducibility and experiment tracking matter. |
| `hardware-iot` | hardware-iot | Software for embedded hardware or IoT devices with physical constraints. |
| `multi-repo-product` | multi-repo-product | A product built from several independent git repositories under one project umbrella. |
| `open-source-library` | open-source-library | A publicly available library with external contributors, published to a package registry. |
| `personal-npm-package` | personal-npm-package | A single maintainer's public npm package — minimal overhead, high automation. |
| `saas-product` | saas-product | A hosted product with paying users, continuous deployment, and operational concerns. |
| `single-repo-monorepo` | single-repo-monorepo | All packages and apps for a product live in one git repository. |
| `solo-developer` | solo-developer | One person writes, reviews, and ships everything. Optimize for speed, not ceremony. |
| `team-project` | team-project | Two or more people collaborate on a shared codebase with explicit coordination norms. |

---

## Template file format

A template file is a `.md` file with a TOML frontmatter block between `---` delimiters.

```
---
id = "my-prc"
version = "1.0.0"
tier = "prc"
stacks = ["rust"]
project_shapes = []
description = "Rust binary repo defaults"
extends = "prc-default"
---

## Coding Conventions

Use `clippy` clean with no warnings. ...

## Testing

All public functions have unit tests. ...
```

### Manifest fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | string | yes | Unique slug. Must match the filename stem. |
| `version` | string | yes | Semver: `MAJOR.MINOR.PATCH`. |
| `tier` | string | yes | One of: `gci` `pci` `apc` `ppc` `prc` `pac` `any`. |
| `stacks` | string[] | yes | Empty array means all stacks. |
| `project_shapes` | string[] | yes | Empty array means all shapes. |
| `description` | string | yes | Short human-readable description. |
| `extends` | string | no | Parent template id. Inherits sections from parent. |
| `min_cascade_version` | string | no | Minimum Cascade version required (semver). |

### Body sections

The body consists of `##`-headed sections. Each heading becomes a named section that the apply engine tracks independently.

```markdown
## Purpose

What this file is for.

## Rules

Specific conventions here.
```

### Placeholders

Use `{{variable_name}}` in the body to mark values the user supplies at apply-time:

```markdown
## Project Contact

Primary maintainer: {{maintainer_email}}
```

Pass values with `--var`:

```bash
cascade template apply --id my-prc --var maintainer_email=alice@example.com
```

Inline defaults work with the `{{name:default}}` form:

```markdown
Primary maintainer: {{maintainer_email:unknown}}
```

### Provenance stamp

When Cascade applies a template, it writes a stamp comment at the top of the target file:

```html
<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-06-10T00:00:00Z" } -->
```

The stamp records which version was applied. Upgrade commands read it to detect when the bundled version is newer.

### Preserve-user-sections semantics

When you upgrade a template, Cascade uses a three-way merge. Sections you edited are preserved unless you pass `--force`. Sections removed from the new template version stay in your file with a deprecation notice; you can delete them at any time.

---

## CLI reference

All template operations go through `cascade template <subcommand>`.

### list

Browse available templates.

```
cascade template list [--tier TIER] [--stack STACK] [--shape SHAPE]
                      [--upgradeable [--target PATH]] [--json]
```

| Flag | Description |
|---|---|
| `--tier TIER` | Filter by tier (`gci`, `pci`, `apc`, `ppc`, `prc`, `pac`, `any`). |
| `--stack STACK` | Filter by stack tag (e.g. `rust`, `tauri`). |
| `--shape SHAPE` | Filter by project shape tag (e.g. `cli-tool`, `lib`). |
| `--upgradeable` | Show only templates with a newer bundled version than the version stamped in the target file. |
| `--target PATH` | Path to the `CASCADE.md` to read stamps from. Defaults to the nearest `.cascade/CASCADE.md` walking up from the current directory. |
| `--json` | Output a JSON array of manifest objects instead of a table. |

### apply

Apply a template to a `CASCADE.md` file.

```
cascade template apply --id ID [--target PATH] [--var KEY=VALUE]... [--dry-run] [--force]
```

| Flag | Description |
|---|---|
| `--id ID` | Template id to apply (e.g. `gci-default`). Required. |
| `--target PATH` | Path to the target `CASCADE.md`. Defaults to the nearest `.cascade/CASCADE.md`. |
| `--var KEY=VALUE` | Substitute `{{KEY}}` in the template body. Repeatable. |
| `--dry-run` | Print what would change without writing anything. |
| `--force` | Overwrite sections that conflict with existing content. |

Apply is idempotent. Running it again on an already-applied template does nothing.

### diff

Show what applying a template would change without writing anything.

```
cascade template diff --id ID [--target PATH]
```

| Flag | Description |
|---|---|
| `--id ID` | Template id to diff. Required. |
| `--target PATH` | Path to the target `CASCADE.md`. Defaults to nearest `.cascade/CASCADE.md`. |

Outputs three groups: sections to be added, sections in conflict, and sections already matching.

### upgrade

Upgrade a previously applied template to the latest bundled version.

```
cascade template upgrade --id ID [--target PATH] [--dry-run] [--force]
```

| Flag | Description |
|---|---|
| `--id ID` | Template id to upgrade. Required. |
| `--target PATH` | Path to the target `CASCADE.md`. Defaults to nearest `.cascade/CASCADE.md`. |
| `--dry-run` | Print what would change without writing anything. |
| `--force` | Overwrite all conflicting sections, including user-edited content. Without `--force`, user edits are preserved. |

Reports three outcome groups: sections updated, sections newly added, and sections deprecated (removed from template but preserved in target).

### create

Scaffold a new user template.

```
cascade template create --id ID [--tier TIER] [--extends PARENT] [--output PATH]
```

| Flag | Description |
|---|---|
| `--id ID` | Unique id for the new template. Required. |
| `--tier TIER` | Tier to pre-fill in the manifest. Defaults to `any`. |
| `--extends PARENT` | Optional parent template id for inheritance. |
| `--output PATH` | Output path. Defaults to `~/.cascade/templates/<id>.md`. |

Refuses to overwrite an existing file. After creating, edit the scaffold, then validate it.

### validate

Check a template file for errors.

```
cascade template validate PATH
```

Validates that the frontmatter parses as a `TemplateManifest`, the `id` matches the filename stem, the `version` is valid semver, the `description` is non-empty, and the body contains at least two `##` sections. Prints `PASS` on success (exit 0) or a numbered error list (exit 1).

### export

Export a template to a standalone `.md` file.

```
cascade template export --id ID [--output DIR]
```

| Flag | Description |
|---|---|
| `--id ID` | Template id to export. Required. |
| `--output DIR` | Directory to write the file into. Defaults to the current directory. |

Writes `<id>.md` with frontmatter and body intact, ready for sharing or submitting as a contribution.

---

## GUI template browser

The template browser lives in the **Templates** tab of the Cascade desktop app.

1. Open the Cascade app. Click **Templates** in the left sidebar.
2. Use the search bar to filter by name or description. Use the dropdown menus to filter by tier, stack, or shape.
3. Click a template row to see its full description and section list.
4. Click **Apply** to apply the template to the `CASCADE.md` for the current project. The app resolves the target file from the active project context.
5. After applying, the **Applied** badge appears on the template row. If a newer bundled version is available, an **Upgrade available** badge appears instead.
6. Click **Diff** to preview changes before applying or upgrading.

The GUI calls the same engine as the CLI. Changes made in the GUI are reflected immediately in the CLI and vice versa.

---

## Inheritance with `extends`

A template can inherit sections from a parent template using the `extends` field:

```toml
extends = "prc-default"
```

When the registry resolves the template, it loads the parent first, then merges in the child's sections. Child sections with the same heading override the parent. This lets you build on a tier default without copying its content.

You can chain extends: `my-rust-prc` → `rust-crate` → `prc-default`. Circular extends are detected and rejected at load time.

---

## Versioning and upgrades

Each template carries a semver `version`. When you apply a template, Cascade stamps the version into the target file. Later, if the bundled template ships a new version, `cascade template list --upgradeable` shows which of your applied templates are behind.

To upgrade:

```bash
# Preview
cascade template upgrade --id gci-default --dry-run

# Apply
cascade template upgrade --id gci-default
```

The three-way merge preserves your edits. Sections removed from the new template get a deprecation comment:

```markdown
<!-- cascade:deprecated section="## Old Section" reason="removed in gci-default v1.2.0" -->
## Old Section

... your content is preserved here ...
```

Delete the section and the comment when you are ready.

---

## Authoring a custom template

1. **Scaffold the file**

   ```bash
   cascade template create --id my-team-prc --tier prc
   ```

   This creates `~/.cascade/templates/my-team-prc.md` with a starter manifest and placeholder sections.

2. **Edit the manifest**

   Open `~/.cascade/templates/my-team-prc.md` in your editor. Set the `description`, adjust `stacks` and `project_shapes` if the template only applies to specific technologies, and add `extends` if you want to build on an existing template.

3. **Write the body**

   Add `##`-headed sections covering the conventions you want every project using this template to follow. Use `{{placeholder}}` syntax for values that vary per project.

4. **Validate**

   ```bash
   cascade template validate ~/.cascade/templates/my-team-prc.md
   ```

   Fix any reported errors, then re-run until you see `PASS`.

5. **Apply to a project**

   ```bash
   cd ~/my-project
   cascade template apply --id my-team-prc
   ```

   User templates in `~/.cascade/templates/` take priority over bundled templates with the same id.

---

## Sharing templates

To share a template with your team or contribute it to the bundled set:

1. Export it to a standalone file:

   ```bash
   cascade template export --id my-team-prc --output ./exported/
   ```

2. Verify the exported file validates cleanly:

   ```bash
   cascade template validate ./exported/my-team-prc.md
   ```

3. Share the `.md` file. Recipients place it in `~/.cascade/templates/` or apply it directly with `--target`.

To contribute to the bundled set, open a pull request against the `acamarata/cascade` repository and place the template in `data/templates/<kind>/<id>.md`. Add an entry to `data/templates/index.json` matching the manifest fields. The CI pack-check job verifies the index is consistent.

---

## Troubleshooting

**`Template 'X' not found`**

Run `cascade template list` to see all available templates. Check that your `~/.cascade/templates/` directory exists if you expect user templates.

**`id field does not match filename slug`**

The `id` field in the TOML frontmatter must exactly match the file's stem (the filename without `.md`). Rename the file or update the `id` field.

**`version is not valid semver`**

Use `MAJOR.MINOR.PATCH` form (e.g. `1.0.0`). Pre-release suffixes like `-alpha.1` are accepted. Partial versions like `1.0` are not.

**`body has N ## sections — at least 2 are required`**

Add at least two `##`-headed sections to the template body. A minimal template needs a `## Purpose` and one other section.

**`missing variable '{{name}}'`**

The template body contains a `{{name}}` placeholder that was not supplied. Pass it with `--var name=value`, or add an inline default (`{{name:default}}`).

**Conflicts reported on apply**

When `apply` reports a conflict, it means a section already exists in your `CASCADE.md` with different content. Use `--force` to overwrite, or edit the target file manually to resolve.

**Upgrade preserved a section I expected to be removed**

Run `cascade template upgrade --force` to discard your edits and take the new template version verbatim. This cannot be undone without git.
