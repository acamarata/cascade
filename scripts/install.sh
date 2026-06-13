#!/bin/bash
# Cascade installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
#
# Environment variables (all optional):
#   CASCADE_VERSION      Install a specific release tag (e.g. v1.0.0).
#                        Defaults to the latest GitHub release.
#   CASCADE_BIN_DIR      Installation directory. Default: ~/.local/bin
#   CASCADE_NO_DAEMON    Set to any non-empty value to skip daemon install.
#   CASCADE_NO_INIT      Set to any non-empty value to skip `cascade init`.
#
# Requirements: curl or wget, sha256sum or shasum, tar.
# Does NOT require sudo. Fails loudly on checksum mismatch.
#
# --help: print this header and exit.
set -eu

REPO="acamarata/cascade"
RELEASES_URL="https://github.com/${REPO}/releases"
API_LATEST="https://api.github.com/repos/${REPO}/releases/latest"

# ── helpers ────────────────────────────────────────────────────────────────────

info()  { printf '\033[1;32m[cascade]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[cascade]\033[0m WARNING: %s\n' "$*" >&2; }
error() { printf '\033[1;31m[cascade]\033[0m ERROR: %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || error "Required tool not found: $1 — please install it and retry."
}

download() {
    # $1 = URL, $2 = destination file
    if command -v curl >/dev/null 2>&1; then
        curl --proto '=https' --tlsv1.2 -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -q --https-only -O "$2" "$1"
    else
        error "Neither curl nor wget found. Install one and retry."
    fi
}

sha256_check() {
    # $1 = file to hash, $2 = expected hex digest
    local file="$1" expected="$2" actual
    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$file" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$file" | awk '{print $1}')
    else
        error "sha256sum / shasum not found — cannot verify checksums."
    fi
    if [ "$actual" != "$expected" ]; then
        error "Checksum mismatch for $(basename "$file").\n  expected: $expected\n  got:      $actual\nAborted — do not use these binaries."
    fi
    info "Checksum OK: $(basename "$file")"
}

help() {
    # Print the header comment block.
    sed -n '/^# Cascade installer/,/^set -eu/{ /^set -eu/d; s/^# \{0,1\}//; p; }' "$0"
    exit 0
}

# ── detect OS + arch ───────────────────────────────────────────────────────────

detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin) os="macos" ;;
        Linux)  os="linux" ;;
        *)      error "Unsupported OS: $(uname -s). Use scripts/install.ps1 on Windows." ;;
    esac

    case "$(uname -m)" in
        arm64|aarch64) arch="arm64" ;;
        x86_64|amd64)  arch="x86_64" ;;
        *)             error "Unsupported architecture: $(uname -m)." ;;
    esac

    echo "${os} ${arch}"
}

# Map OS+arch to the release asset name.
# Asset pattern from .github/workflows/release.yml:
#   cascade-{VERSION}-{target}.tar.gz
# Targets produced by release.yml:
#   macOS arm64   -> universal-apple-darwin (lipo universal, preferred)
#   macOS x86_64  -> x86_64-apple-darwin    (fallback if universal not present)
#   Linux x86_64  -> x86_64-unknown-linux-gnu
#   Linux arm64   -> aarch64-unknown-linux-gnu
asset_name() {
    local os="$1" arch="$2" version="$3"
    case "${os}-${arch}" in
        macos-arm64|macos-x86_64)
            # Prefer the universal binary; fall back to arch-specific.
            echo "cascade-${version}-universal-apple-darwin.tar.gz"
            ;;
        linux-x86_64)
            echo "cascade-${version}-x86_64-unknown-linux-gnu.tar.gz"
            ;;
        linux-arm64)
            echo "cascade-${version}-aarch64-unknown-linux-gnu.tar.gz"
            ;;
        *)
            error "No prebuilt binary for ${os}/${arch}. Build from source: cargo install cascade-cli"
            ;;
    esac
}

# ── version resolution ─────────────────────────────────────────────────────────

resolve_version() {
    if [ -n "${CASCADE_VERSION:-}" ]; then
        # Strip leading 'v' for display, keep full tag for URLs.
        echo "${CASCADE_VERSION}"
        return
    fi
    info "Resolving latest release version..."
    local tag
    if command -v curl >/dev/null 2>&1; then
        tag=$(curl --proto '=https' --tlsv1.2 -fsSL "$API_LATEST" \
              | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    else
        tag=$(wget -q --https-only -O - "$API_LATEST" \
              | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    fi
    [ -n "$tag" ] || error "Could not determine latest release. Set CASCADE_VERSION=v<N> and retry."
    echo "$tag"
}

# ── main ───────────────────────────────────────────────────────────────────────

main() {
    case "${1:-}" in
        --help|-h) help ;;
    esac

    # Dependencies
    need tar

    # Platform detection
    local platform
    platform=$(detect_platform)
    local os arch
    os=$(echo "$platform" | awk '{print $1}')
    arch=$(echo "$platform" | awk '{print $2}')
    info "Platform: ${os}/${arch}"

    # Version
    local version tag
    tag=$(resolve_version)
    version="${tag#v}"
    info "Version: ${tag}"

    # Asset
    local primary_asset fallback_asset
    primary_asset=$(asset_name "$os" "$arch" "$version")
    # For macOS, fall back to arch-specific if universal is absent.
    if [ "$os" = "macos" ] && [ "$arch" = "x86_64" ]; then
        fallback_asset="cascade-${version}-x86_64-apple-darwin.tar.gz"
    elif [ "$os" = "macos" ] && [ "$arch" = "arm64" ]; then
        fallback_asset="cascade-${version}-aarch64-apple-darwin.tar.gz"
    else
        fallback_asset=""
    fi

    # Temp dir
    local tmp
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    local base_url="${RELEASES_URL}/download/${tag}"

    # Download SHA256SUMS
    info "Downloading SHA256SUMS..."
    download "${base_url}/SHA256SUMS" "${tmp}/SHA256SUMS"

    # Download primary asset (try universal for macOS, else arch-specific)
    local tarball="${tmp}/${primary_asset}"
    local resolved_asset="$primary_asset"
    info "Downloading ${primary_asset}..."
    if ! download "${base_url}/${primary_asset}" "$tarball" 2>/dev/null; then
        if [ -n "$fallback_asset" ]; then
            warn "Universal binary not found; trying arch-specific: ${fallback_asset}"
            resolved_asset="$fallback_asset"
            tarball="${tmp}/${fallback_asset}"
            download "${base_url}/${fallback_asset}" "$tarball" \
                || error "Could not download ${fallback_asset} from ${base_url}. Check ${RELEASES_URL} for available assets."
        else
            error "Could not download ${primary_asset} from ${base_url}. Check ${RELEASES_URL} for available assets."
        fi
    fi

    # Verify checksum
    info "Verifying checksum..."
    local expected
    expected=$(grep " ${resolved_asset}$" "${tmp}/SHA256SUMS" | awk '{print $1}')
    [ -n "$expected" ] \
        || error "Checksum entry for '${resolved_asset}' not found in SHA256SUMS. Check that the release is complete."
    sha256_check "$tarball" "$expected"

    # Extract
    info "Extracting..."
    tar -xzf "$tarball" -C "$tmp"

    # Install destination
    local bin_dir="${CASCADE_BIN_DIR:-$HOME/.local/bin}"
    mkdir -p "$bin_dir"

    # Install binaries
    local installed=0
    for bin in cascade cascaded; do
        if [ -f "${tmp}/${bin}" ]; then
            chmod +x "${tmp}/${bin}"
            cp "${tmp}/${bin}" "${bin_dir}/${bin}"
            info "Installed: ${bin_dir}/${bin}"
            installed=$((installed + 1))
        fi
    done
    [ "$installed" -gt 0 ] || error "No binaries found in the tarball. The release asset may be malformed."

    # PATH advice
    case ":${PATH}:" in
        *":${bin_dir}:"*) : ;;  # already on PATH
        *)
            warn "${bin_dir} is not on your PATH."
            info "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
            # shellcheck disable=SC2016  # $PATH intentionally unexpanded (literal advice string)
            printf '\n    export PATH="%s:$PATH"\n\n' "${bin_dir}"
            ;;
    esac

    # Post-install steps
    if [ -z "${CASCADE_NO_DAEMON:-}" ]; then
        info "Installing daemon service..."
        if "${bin_dir}/cascade" daemon install 2>&1; then
            info "Daemon installed and started."
        else
            warn "'cascade daemon install' failed — you may need to start the daemon manually."
        fi
    else
        info "Skipping daemon install (CASCADE_NO_DAEMON is set)."
    fi

    if [ -z "${CASCADE_NO_INIT:-}" ]; then
        info "Running cascade init..."
        if "${bin_dir}/cascade" init --accept-defaults 2>&1; then
            info "Init complete."
        else
            warn "'cascade init --accept-defaults' failed — run it manually when your shell is configured."
        fi
    else
        info "Skipping init (CASCADE_NO_INIT is set)."
    fi

    info "Running verification..."
    if "${bin_dir}/cascade" verify 2>&1; then
        info "cascade verify passed."
    else
        warn "'cascade verify' reported issues — run it again after configuring your PATH."
    fi

    info "Done. Run 'cascade --version' to confirm."
}

main "$@"
