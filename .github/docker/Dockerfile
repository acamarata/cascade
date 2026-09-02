# Purpose: OCI image for cascade's SERVER profile (P1-E01-W1-S01-T6, per
#   R-14.119 — this file's location is a binding T0 ruling, not a free
#   choice: Art.10.1's clean-root allowlist has no root-Dockerfile entry, and
#   .github/ is where Art.10.1 already re-homes CI-adjacent infrastructure
#   (SECURITY.md, CONTRIBUTING.md, issue/PR templates), so the release image
#   joins that set instead of amending the allowlist).
# Inputs:   a single pre-built, statically linked (CGO_ENABLED=0) `cascade`
#   binary supplied as build context by goreleaser's `dockers:` stanza (see
#   .goreleaser.yaml) — this Dockerfile never runs `go build` itself. The
#   binary is profile-agnostic: "server profile" (02-TARGET-STRUCTURE.md
#   "Profiles" — Postgres/pgvector/S3/Redis vs local SQLite/fs) is a runtime
#   config selection, not a build-time split, so the image ships the exact
#   same bytes goreleaser's archives ship.
# Outputs:  a minimal container image whose only executable is `cascade`.
#   No shell, no package manager: distroless static base, so there is
#   nothing in the image an attacker can pivot to.
# Constraints: non-root (the distroless `:nonroot` tag runs uid/gid 65532);
#   no CGO/dynamic linking (matches .goreleaser.yaml's CGO_ENABLED=0); no
#   secrets or key material baked in — server-profile config (DB/S3/Redis
#   credentials) is supplied entirely via runtime env/config-file mounts,
#   never at build time. Registry push and cosign attestation are CI-side
#   only (R-14.119, §D-16) — this file only describes what gets built.
# SPORT: build/release-train (ADD) — see T-6's sport_updates placeholder;
#   taxonomy undefined until N/S-28.T1 lands.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/acamarata/cascade" \
      org.opencontainers.image.title="cascade" \
      org.opencontainers.image.description="Cascade: a local-first AI agent runtime — one binary, CLI and daemon. This image runs the server profile (Postgres/pgvector/S3/Redis)." \
      org.opencontainers.image.licenses="MIT"

# goreleaser's docker build context places the platform-matched archive
# binary at the repo root of the build context under this exact name (see
# .goreleaser.yaml dockers[].extra_files / binary naming for id: cascade).
COPY cascade /usr/local/bin/cascade

# The distroless `:nonroot` base already runs as uid/gid 65532 (user
# "nonroot"); no USER directive is needed and none is added, so a future
# base-image swap cannot silently drop back to root without CI's SBOM/user
# check (docs/developer/release.md) catching it.

ENTRYPOINT ["/usr/local/bin/cascade"]
