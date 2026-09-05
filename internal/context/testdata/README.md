# internal/context fixtures: provenance index

This is the package-level provenance entry point for every fixture under
`internal/context/testdata/`. Per Art.2.2 a golden is only worth what its
provenance says, so each directory below states the tool, version, date and
exact command that produced it, and every deliberate edit to a captured
byte.

| Directory | What it holds | Provenance |
|---|---|---|
| `v1-goldens/` | discovery scenarios, merge inputs and expectations, and the first harness writer's per-tier expected output | `v1-goldens/README.md` |
| `goldens/cx/` | captured behaviour of the first AGENTS.md harness | this file, below |
| `goldens/oc/` | captured behaviour of the second AGENTS.md harness | this file, below |

---

## goldens/cx: the first AGENTS.md harness

Captured 2026-09-05 from the tool's own binary, version `0.144.2` as
reported by its `--version` flag.

Product names appear below only inside command lines and inside the captured
bytes themselves. A provenance stamp that hides which tool it came from is
not a provenance stamp, and a capture edited to remove the tool's own words
is no longer a capture. Everywhere else, in this file and in all shipped
code, the targets are named by their neutral abbreviations.

### `prompt-input.capture.txt`

- command: `codex debug prompt-input`
- workspace: a scratch tree built for the capture, holding one `AGENTS.md`
  per cascade tier, each carrying a unique marker (`GCIMARK` ... `PAIMARK`).
  The global file was placed at `$CODEX_HOME/AGENTS.md`; the other four at
  the grandparent, parent, git root and a directory below the git root.
- environment: `HOME` and `CODEX_HOME` both pointed into the scratch tree,
  so the capture never read the developer's real configuration.
- extraction: the command emits JSON; the fixture is the single instruction
  string from that JSON, verbatim.
- one documented edit: the workspace's absolute path is replaced by
  `<WORKSPACE>`, so no machine-specific path is committed. Nothing else was
  changed.

What it establishes, and what the tests assert against it:

1. The file the tool reads is called `AGENTS.md`.
2. Its bytes are taken **verbatim**: the markers appear unchanged inside the
   tool's own envelope, so a generator must emit plain Markdown and no
   envelope of its own.
3. The concatenation order is **most general first**: the global file, then
   the git root's, then the nested one. Cascade's own emission order has to
   agree with this or the two disagree about which instruction wins.
4. **Reach**: only three of the five markers appear. The tool reads the
   global file plus the files from the git root down to the working
   directory; it does NOT read the two tiers ABOVE the git root. The
   generator still renders those tiers, because dropping a tier silently is
   the failure this package exists to prevent, but WHERE an out-of-reach
   tier's file should be placed is a write-path question and is left to the
   sync-command ticket rather than guessed at here.

### `codex-home.capture.txt`

- command: `codex doctor`, filtered to the `CODEX_HOME` row.
- what it establishes: the tool's home directory, and therefore the global
  tier's file name, defaults to `~/.codex`. The test derives the generator's
  global-tier name from this row rather than restating it.
- a `CODEX_HOME` environment override moves that directory. Resolving
  environment overrides is the write path's job, not a pure generator's;
  this is a known gap handed to the sync-command ticket, not an oversight.

---

## goldens/oc: the second AGENTS.md harness

Captured 2026-09-05 from the tool's own shipped bundle, version `1.15.5` as
reported by its `--version` flag.

### `instruction-systempaths.capture.txt`

- command: `strings` over the shipped single-file bundle, filtered to the
  three fragments of the tool's own instruction-path resolver
  (`Instruction.systemPaths` and the two candidate lists it iterates).
- verbatim: the fragments are the shipped bytes, unedited. They are minified
  because the bundle is.
- why source rather than a run: the tool exposes no command and no server
  route that reports which instruction files it resolved. `debug config`,
  `debug info`, `debug agent` and the headless server's `/agent`, `/config`
  and `/path` routes were all checked and none of them names them. The
  shipped resolver is therefore the closest available real counterpart, and
  it is a stronger one than a self-authored guess: it is the code that runs.
  This is recorded plainly rather than presented as a run capture.

What it establishes, and what the tests assert against it:

1. The candidate list is `["AGENTS.md", "CLAUDE.md", "CONTEXT.md"]`, with
   `AGENTS.md` first. The list is iterated with a `break` on the first name
   that matches anywhere in the walk, so an `AGENTS.md` present anywhere
   means the other two are never consulted.
2. The global instruction file is `AGENTS.md` under the tool's config
   directory.
3. The project walk is `findUp(name, directory, worktree)`: it climbs from
   the working directory to the repository root and stops there. As with the
   first harness, the two tiers above the git root are out of reach, and the
   same note applies.

### `debug-paths.capture.txt`

- command: `opencode debug paths`, run in the same kind of scratch tree with
  `HOME` and `XDG_CONFIG_HOME` pointed into it.
- two documented edits: the scratch home is replaced by `<HOME>` and the
  system temporary directory by `<TMPDIR>`, so no machine-specific path is
  committed. Nothing else was changed.
- what it establishes: the config directory is `<HOME>/.config/opencode`,
  from which the test derives the generator's global-tier file name.
- `XDG_CONFIG_HOME` moves that directory; same write-path note as above.

---

## What these captures changed about the ticket's premise

The ticket assumed two further instruction FORMATS. The captures say the two
tools share one: the same file name, plain Markdown, taken verbatim, with no
schema. The two writers therefore share one renderer and differ only in
where the global tier's file goes. Recorded here because a reviewer
comparing the shipped code against the contract will otherwise read the
single renderer as a shortcut rather than as the finding it is.
