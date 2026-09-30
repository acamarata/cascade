# Supply-chain tooling: pins and accepted residuals

The third-party tools this ticket (P1-SHIP-14) pinned by version and
verified checksum or module-proxy checksum, where each pin lives, and what
it does not cover — plus, in a second table below, the tools
`.github/workflows/release.yml` and `.github/workflows/supply-chain.yml`
install that are **not** pinned to a verified digest today, recorded as
residual risk rather than left silent. Scope is exactly those two
workflows; tool installs in the other workflows are listed under "What
this register does not cover" below. Written for whoever
reviews the next tool addition, so the bar an existing pin already clears
is explicit rather than reconstructed from the workflow YAML each time.

## Why a register, not just pinned YAML

A pin in a workflow file answers "which version." It does not answer
"pinned against what," "who verified it," or "what does this NOT protect
against" -- and a reviewer who has to re-derive those from a `curl` line
and a sha256 literal will eventually get it wrong. This page is the
answer key: one row per tool, plus the residual risk the pin leaves open
on purpose.

## Register

| Tool | Pin | Verified by | Workflow / step | Residual |
|---|---|---|---|---|
| syft (SBOM generator) | release `v1.52.0`, asset `syft_1.52.0_linux_amd64.tar.gz` | sha256 checked against the literal `caeedb81fb0491615f1ebd1761e4145d41ee86dd2cc7bf80669f9f5ad9d6133d`, copied verbatim from syft's own `syft_1.52.0_checksums.txt` release asset (never computed from a download in the same run) | `.github/workflows/release.yml`, both `verify` and `snapshot` jobs, "Install syft" step | A future syft release is not pulled in automatically -- the version bump is a manual, reviewed edit to this pin, same as any other dependency bump. |
| govulncheck (`golang.org/x/vuln/cmd/govulncheck`) | module version `v1.8.0` | `go install` resolves the module through the Go module proxy/checksum database (`sum.golang.org`) the same way any pinned Go module dependency is verified; no separate binary checksum is published for this tool | `.github/workflows/supply-chain.yml`, `govulncheck` job, "Install govulncheck" step | **The vulnerability database is not pinned and is not meant to be.** `govulncheck` fetches its advisory data from `vuln.go.dev` at run time on every invocation, independent of the tool's own version. Pinning the tool version does not freeze which vulnerabilities it reports -- a run today and a run next month against the identical pinned tool can report different results because the database moved, not the code. This is accepted by design: the entire point of this lane is a scan against a current database. **Review trigger:** if `govulncheck ./...` ever needs to be reproduced byte-for-byte (e.g. to explain a historical CI result), the database state at that point in time cannot be recovered from the pin alone -- only the tool version can. Re-pin the tool version at least whenever a CVE in `govulncheck` itself is disclosed, or when the module's release notes describe a scanning-behavior change. |
| go-licenses (`github.com/google/go-licenses`) | module version `v1.6.0` (pre-existing pin, unchanged by this page) | same module-proxy checksum verification as govulncheck above | `.github/workflows/supply-chain.yml`, `licenses` job, "Install go-licenses" step | Classifies each dependency's LICENSE file text directly; `modernc.org/mathutil` is hand-verified and ignored in this one check because its BSD-3-Clause wording does not match the tool's template (see the inline comment at `supply-chain.yml` for the full explanation). That exemption is scoped to one module and reviewed the same way any allowlist entry is. |

## Residual: tools installed without a verified pin

Tools `release.yml` and `supply-chain.yml` install where no exact-version
pin, checksum, or digest check is applied today. Each row is a deliberate gap, not an
oversight left off the register above — the point of writing it down is
that a blank residual column reads as "not reviewed," not as "safe" (see
"Adding a new pinned tool" below).

| Tool | Where | What floats | Verified by | Residual |
|---|---|---|---|---|
| goreleaser (the `goreleaser` binary itself, distinct from the `goreleaser-action` wrapper, which IS pinned to a full commit SHA) | `.github/workflows/release.yml`, "Install goreleaser" step, both the `verify` and `snapshot` jobs | Neither step passes a `version:` input to `goreleaser/goreleaser-action@e435ccd...` (v6.4.0), so the action installs whatever its own default constraint resolves to at run time — `~> v2` per the action's published default [Certain: no `version:` key is present in either step]. | Not verified by this page's authors against a checksum or digest; a read of the action's own source found no checksum/verify step in its install path [Likely, not exhaustively audited]. | The exact `goreleaser` build produced by a run is not reproducible from this pin alone, and a future `v2.x` release is pulled in automatically without a reviewed edit here. Pinning `version:` to an exact goreleaser release plus a verified checksum is a follow-up (tracked as a planning finding out of this ticket's review, P1-SHIP-14 cr-opus P1; not done by this ticket). |
| minisign | `.github/workflows/release.yml`, "Install minisign" step (`snapshot` job only) | Installed via `apt-get install -y minisign` with no version pin; whatever version is current in the runner's Ubuntu apt repositories at run time. | Trusted only via the apt package's own repository signing (Ubuntu/Debian archive signatures) — not independently verified by this workflow with a separate checksum or digest. | The minisign binary version is not reproducible across runs, and a compromised or yanked apt package would not be caught by anything in this workflow. No follow-up ticket filed yet. |
| QEMU / Docker Buildx setup actions' helper images (`docker/setup-qemu-action`, `docker/setup-buildx-action`) | `.github/workflows/release.yml`, "Set up QEMU" and "Set up Docker Buildx" steps (`snapshot` job) | The `uses:` lines for both actions are pinned to full commit SHAs (same convention as every other action in these workflows), but neither step passes an explicit image tag, so the underlying QEMU emulation and buildx builder images each action pulls are that action's own floating default. | Not independently verified by this workflow. | A future default-image change in either upstream action changes what cross-arch build environment is used without a reviewed edit to this repo. No follow-up ticket filed yet. |

## What this register does not cover

- **Tool installs in the other workflows.** Not reviewed by this page:
  `ci.yml` installs apt packages with no version pin (`libpam0g-dev` in
  the `linux-pam-build` job; `openssh-server` in the
  `node-tunnel-real-sshd`, `node-provision-real-counterparts` and
  `sync-chunked-transfer-real-sshd` jobs, plus `minisign` in
  `node-provision-real-counterparts`), and `bench.yml`'s `benchstat`
  job runs `go install golang.org/x/perf/cmd/benchstat@<pseudo-version>`
  (a module-version pin, verified through the Go checksum database like
  the module pins above). None of these jobs sign or publish a release.

- **GitHub Actions (`uses:` steps).** Every third-party action in
  `release.yml` and `supply-chain.yml` is pinned to a full commit SHA with
  the resolved tag in a trailing comment (both workflows' header
  comments). That convention predates this ticket and is not repeated
  here as a register row -- it is enforced inline, at the point of use,
  which is where a reviewer already has to look to change it.
- **Go module dependencies** (`go.mod`/`go.sum`). Covered by
  `go.sum`'s own cryptographic pinning plus the `dependency-review`
  job (PR-only, GitHub's advisory-database diff) and the `licenses` job
  above -- a different mechanism from the manual-install tools this page
  tracks, and out of scope here.
- **The private-tracked-path gate** (`internal/build/privatepaths.go`,
  `TestNoPrivatePathsTracked_Live`/`_SeededForceAdd`). Not a supply-chain
  tool pin; it is the companion control landed by the same ticket
  (P1-SHIP-14) that stops a private planning path (`.claude/`,
  `.opencode/`, `.cascade/`) from ever being tracked, including via
  `git add -f`, which bypasses `.gitignore` and cannot be refused by any
  hook. See that file's package doc for the full rationale.

## Adding a new pinned tool

1. Pick the exact version, never a floating tag or `@latest`.
2. Get the checksum (or equivalent verification) from the tool's own
   published release artifact -- never compute it from a download inside
   the same CI run that would then trust it.
3. Add a row to the register above before merging the workflow change.
4. State the residual explicitly, even if it is "none identified." A
   blank residual column reads as "not reviewed," not as "safe."
