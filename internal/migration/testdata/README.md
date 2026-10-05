# Migration fixtures

`v1-home/` is a dot-free source home derived from the committed v1 golden
inputs. Its README records source paths, SHA-256 checksums and the vault-name
rename rule. The tests materialize `.cascade/` only under a temporary home.

The committed values remain redacted. Integration tests replace each marker
with a logged `NONSECRET-<relative-path-slug>-<occurrence>` value in the
temporary derivative after checking custody isolation. Raw fixtures must fail
the production vault import refusal.
