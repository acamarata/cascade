# Sample Workspace

A complete worked example of a Cascade-managed project structure. This shows how
the six-tier instruction cascade looks on disk for a typical software project.

## What is here

```
sample-workspace/
├── .cascade/                    # Cascade working memory (gitignored in real projects)
│   ├── memory/
│   │   ├── decisions.md         # Technical decisions and rationale
│   │   ├── lessons.md           # Gotchas discovered during development
│   │   └── patterns.md          # Codebase conventions
│   └── CASCADE.md               # Project-level cascade (PPC tier)
├── CASCADE.md                   # Repo-level cascade (PRC tier) — symlinked to AI tool files
└── README.md                    # This file
```

## Tier overview

In a real project, this workspace sits inside a larger structure:

```
~/.cascade/CASCADE.md            # GCI — global rules, all projects
~/Sites/.cascade/CASCADE.md      # APC — all coding projects
~/Sites/myproject/.cascade/CASCADE.md   # PPC — project-level rules
~/Sites/myproject/myrepo/CASCADE.md     # PRC — this repo
~/Sites/myproject/myrepo/web/CASCADE.md # PAC — a specific app inside the repo
```

Each tier adds specifics without restating what the parent already said.

## Try it

```bash
# Initialize a new workspace using this example as a template
cascade init --from examples/sample-workspace

# Or use the interactive wizard
cascade wizard
```

## Key concepts shown

- The PRC-tier CASCADE.md covers only this repo. Cross-repo decisions live in PPC.
- Memory files accumulate knowledge over time. They are read by AI agents at session start.
- The `decisions.md` file records why choices were made, not just what was chosen.
- Pattern files prevent drift: AI agents read them before implementing anything.
