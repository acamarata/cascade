#!/usr/bin/env bash
# build.sh — compile the plugin to WASM and copy to .cascade/plugins/
# Usage: ./build.sh [--release]
set -euo pipefail

PROFILE="${1:---debug}"
if [[ "$PROFILE" == "--release" ]]; then
    CARGO_ARGS="--release"
    PROFILE_DIR="release"
else
    CARGO_ARGS=""
    PROFILE_DIR="debug"
fi

PLUGIN_DIR="${HOME}/.cascade/plugins"
mkdir -p "${PLUGIN_DIR}"

cargo build --target wasm32-wasip1 ${CARGO_ARGS}

# Copy the compiled .wasm to the local plugin dir.
WASM_SRC="target/wasm32-wasip1/${PROFILE_DIR}/$(basename "$(pwd)" | tr '-' '_').wasm"
WASM_DST="${PLUGIN_DIR}/$(basename "$(pwd)").wasm"

cp "${WASM_SRC}" "${WASM_DST}"
echo "Plugin built: ${WASM_DST}"
