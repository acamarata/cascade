# v1 config fixture provenance

`daemon-known.toml` is copied byte-for-byte from the v1 archive test literal at
`../cascade-v1/crates/cascade-daemon/src/config.rs:783`.

`daemon-integration.toml` is copied byte-for-byte from
`../cascade-v1/crates/cascade-daemon/tests/fixtures/test-config.toml`.

Both were harvested on 2026-09-12 and are importer inputs, never generated
outputs. The larger fixture deliberately includes currently unmappable sections
and proves the translator quarantines and refuses them without writing.
