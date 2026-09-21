# Wiki fixture provenance (Art.2)

`fixtures/hijri-core-wiki.bundle` is a real GitHub wiki git repository,
captured with the real `git` binary against a real, public GitHub wiki —
never a self-authored dialect.

| Field | Value |
|---|---|
| Tool | `git bundle create <file> --all` |
| Git version | `git version 2.51.0` (darwin/arm64) |
| Date captured | 2026-09-21 |
| Source repo slug | `acamarata/hijri-core` |
| Source wiki URL | `https://github.com/acamarata/hijri-core.wiki.git` |
| Ref captured | `refs/heads/master` at `e202d6ef7a049fcc7e43779ab1b635630e9975b7` (2026-05-30) |
| Bundle contents | 4 refs (`HEAD`, `refs/heads/master`, `refs/remotes/origin/HEAD`, `refs/remotes/origin/master`), complete history |

Capture command, run against a full (non-shallow) clone of the real wiki:

```
git clone https://github.com/acamarata/hijri-core.wiki.git hijri-core-wiki
cd hijri-core-wiki
git bundle create hijri-core-wiki.bundle --all
git bundle verify hijri-core-wiki.bundle
```

`acamarata/hijri-core` is chosen because it is a real, public repository
this same GitHub org owns (see `.claude/CLAUDE.md`'s npm-maintainer-org
note), so the fixture's content is unambiguously safe to check in and
needs no license clearance beyond the repository's own.

The bundle carries 31 real wiki pages (`Home.md`, `Architecture.md`,
`api/functions/*.md`, `guides/*.md`, etc.) under a single commit. Tests
clone FROM this bundle with the real `git` binary (`git clone <bundle>
<tempdir>`), never from `github.com` — no network reaches a unit test.

Do not regenerate this fixture by hand-authoring wiki content: replace it
only by re-running the capture command above against a real wiki and
updating this table.
