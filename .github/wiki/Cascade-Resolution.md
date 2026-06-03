# Cascade Resolution Engine

The 6-tier cascade resolution engine (`cascade-core::cascade_resolve`) loads and merges
configuration from all six cascade tiers in priority order and returns a `ResolvedContext`.

## Tiers

| # | Tier | Acronym | Default path |
|---|------|---------|--------------|
| 1 (highest) | Global Cascade Instructions | GCI | `~/.cascade` |
| 2 | Personal Cascade Instructions | PCI | `~/Downloads/.cascade` |
| 3 | All-Projects Cascade | APC | `~/Sites/.cascade` |
| 4 | Per-Project Cascade | PPC | `{project}/.cascade` |
| 5 | Per-Repo Cascade | PRC | `{repo}/.cascade` |
| 6 (lowest) | Per-App Cascade | PAC | `{app}/.cascade` |

GCI has the highest precedence. When two tiers define the same scalar (e.g. `instructions`),
the higher-tier value wins.

## Merge semantics

| Field | Rule |
|---|---|
| `instructions` | Highest-tier non-empty value wins |
| `rules` | Accumulated from all tiers; GCI rules appear first |
| `memory_paths` | Accumulated; GCI paths appear first |
| `task_paths` | Accumulated; GCI paths appear first |

Missing tiers (no `.cascade/` directory) are silently skipped. Resolution always succeeds
unless an existing file cannot be read.

## Configuration format

Each tier directory may contain:

- `config.toml` — structured config (preferred)
- `CASCADE.md` — free-text instructions (fallback when `config.toml` has no `instructions` key)

Example `config.toml`:

```toml
instructions = "Always use snake_case identifiers in this project."
rules = ["Never commit secrets", "Run tests before push"]
memory_paths = [".cascade/memory/decisions.md"]
task_paths = [".cascade/tasks/active.md"]
```

## API

```rust
use cascade_core::cascade_resolve::resolve_cascade;
use std::path::Path;

fn main() -> cascade_types::error::Result<()> {
    let ctx = resolve_cascade(Path::new("/my/project"))?;
    println!("instructions: {}", ctx.instructions);
    println!("tiers found: {}", ctx.tier_sources.len());
    for (tier, path) in &ctx.tier_sources {
        println!("  {tier}: {}", path.display());
    }
    Ok(())
}
```

## Environment overrides

| Variable | Effect |
|---|---|
| `CASCADE_APC_PATH` | Override the APC tier path (default: `~/Sites/.cascade`) |

## ResolvedContext fields

| Field | Type | Description |
|---|---|---|
| `instructions` | `String` | Merged instruction text (highest-tier wins) |
| `rules` | `Vec<String>` | All rules, GCI-first |
| `memory_paths` | `Vec<PathBuf>` | All memory file paths, GCI-first |
| `task_paths` | `Vec<PathBuf>` | All task file paths, GCI-first |
| `tier_sources` | `BTreeMap<TierName, PathBuf>` | Which tiers were found and where |
| `resolved_at` | `String` | ISO-8601 UTC timestamp |

## Harness compatibility file generation

After resolution, `cascade-core::compat_gen` can generate `CLAUDE.md` and `AGENTS.md` files
at each resolved tier directory so Claude Code and OpenCode both pick up the cascade instructions.

```rust
use cascade_core::compat_gen::generate_all;
use std::path::Path;

fn main() -> cascade_types::error::Result<()> {
    let files = generate_all(Path::new("/my/project"))?;
    for f in &files {
        println!("wrote: {}", f.path.display());
    }
    Ok(())
}
```

### Idempotency

The generator checks the first line of any existing `CLAUDE.md` for the marker
`# cascade:generated — do not edit by hand`. If the marker is present, the file is
regenerated. If the marker is absent (hand-written instructions), the file is skipped with
a warning and no changes are made.

### Platform behavior

| Platform | `AGENTS.md` format |
|---|---|
| Unix (macOS, Linux) | Relative symlink pointing to `CLAUDE.md` in the same directory |
| Windows | Plain file with identical content to `CLAUDE.md` |

## Related

- [Six-Tier-Taxonomy](Six-Tier-Taxonomy.md) — full tier naming reference
- [Architecture](Architecture.md) — daemon architecture overview
