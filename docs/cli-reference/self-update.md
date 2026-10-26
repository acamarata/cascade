# `cascade self-update`

`cascade self-update [--check] [--json]` updates an `install.sh` install to the
latest signed release. It verifies the release with the key embedded in the
binary, swaps the executables while keeping the previous set, hands a running
daemon to the new binary, and restores the previous set if any step fails.

## What it does, in order

1. Resolves the install channel: the build stamp, then a Homebrew Caskroom
   path, then the install receipt's `channel`, else `manual`.
2. For a `script` install, checks every file in the receipt's `files[]`
   against `install_dir` by sha256. A file that differs stops the run with
   `install differs from receipt` and the remedy `rerun install.sh`. Nothing is
   fetched or written.
3. Finds the latest stable tag (the `releases/latest` redirect).
4. Downloads `cascade_checksums.txt` and its `.minisig` and verifies them with
   the embedded release key.
5. Picks exactly one archive line for this platform
   (`cascade_<version>_<os>_<arch>.tar.gz` or `.zip`). SBOM lines never match.
6. Reads the version from the signed trusted comment
   (`cascade <version> checksums`). It must equal the archive name's version,
   and the tag unless `CASCADE_UPDATE_DIST` is set, else the run refuses with
   `version mismatch`.
7. Compares that version by semver precedence with the running one and with
   the receipt's `release_tag`. Older than either refuses with `downgrade`. Equal, or an archive digest equal to the
   receipt's `archive_sha256`, is `none`: nothing is downloaded.
8. Downloads the signed checksums again with the archive. They must name the
   same version and archive digest step 7 chose, and the downgrade rule is
   applied again; otherwise the run refuses and changes nothing. Checks the
   archive's sha256 and extracts it into a private work directory. Absolute paths, `..`, links and special files refuse.
9. Stages each executable as `<install_dir>/.<name>.new` (mode 0755, synced).
10. Copies the current files and the current receipt (`receipt.json`) to
    `<data dir>/update/previous/`, all synced, then renames the helper and the
    plugin into place, and `cascade` last.
11. If a daemon is running, sends it one `SIGUSR2` (the hand-off signal). The
    daemon drains and relaunches on the new binary. The command then polls
    `status.get` every 250 ms for up to 30 s until it reports the new version.
12. Rewrites the install receipt.

Any failure after step 10 restores the previous files. A daemon that does not
come back on the new version within 30 s also restores them and exits with
`unavailable`, naming `cascade daemon start`. When the daemon was already
signalled, the error also says it may be running the new binary and names
`cascade daemon restart`; no second signal is sent. If a restore itself fails,
the error names the remedy `rerun install.sh`.

An interrupted swap (the process killed between two renames) can leave a mixed
install: the next run refuses it with `install differs from receipt`, the
remedy is to rerun `install.sh`, and the previous set and its receipt stay in
`<data dir>/update/previous/`.

Only one self-update runs at a time. Steps 2 to 12 hold the lock
`<data dir>/update/lock`; a second run refuses with
`another self-update is running` and changes nothing. `--check` takes no lock.

On Windows there is no daemon. The old files are renamed to `<name>.old` and
the new ones take their place; the next start runs the new version. A restore
on Windows uses renames only: the new file moves to `<name>.failed-<n>` and
`<name>.old` is renamed back, so a running image is never deleted.

## Channels

| Channel | Behaviour |
|---|---|
| `script` | Updates as above. |
| `brew` | Prints `managed by brew; run brew upgrade cascade`. Changes nothing. |
| `oci` | Prints `managed by oci; pull the image`. Changes nothing. |
| `node-managed` | Prints `managed by the controller (node upgrade)`. Changes nothing. |
| `manual` | No receipt: prints how to reinstall with `install.sh`. Changes nothing. |

Each of these exits 0. A build without a release stamp (`dev`) refuses to
update unless `CASCADE_UPDATE_DIST` is set.

## `--check`

Reports what the command would do and changes nothing: no download of the
archive, no write, no signal. With `--json` the envelope's `data` is:

```json
{"channel":"script","reason":"install receipt /Users/me/.cascade/install-receipt.json",
 "action":"update",
 "current":{"release_tag":"v2.0.0","archive_sha256":"<hex>","version":"2.0.0"},
 "latest":{"release_tag":"v2.0.1","archive_sha256":"<hex>","version":"2.0.1"},
 "compatibility":null}
```

`action` is one of `none`, `update`, `noop-channel` or `defer-controller`.
`compatibility` is always `null` for now. `base_url_host` appears when
`CASCADE_UPDATE_BASE_URL` is set. Without `--check`, `--json` adds `updated`,
`daemon_signalled` and `previous_dir`.

## Environment

| Variable | Meaning |
|---|---|
| `CASCADE_UPDATE_BASE_URL` | Release base. Default `https://github.com/acamarata/cascade/releases`. HTTPS only; plain HTTP only for `127.0.0.1` or `localhost`. Files come from `<base>/download/<tag>/`. |
| `CASCADE_UPDATE_DIST` | A local goreleaser `dist/` directory to update from (test lane). The tag comes from its `metadata.json`. |
| `CASCADE_MINISIGN_PUBKEY` | A minisign public key file that replaces the embedded release key (test lane). With `CASCADE_UPDATE_BASE_URL` and `CASCADE_UPDATE_DIST` it lets whoever sets the environment choose the trust root and the release source. For an install your own user owns, that is no more than that user can already do. Never run self-update under `sudo -E` or any privileged runner that keeps the caller's environment. |
| `CASCADE_HOME` | Where the receipt and the data dir live. Default `$HOME/.cascade`. |

## Network

Every request, redirects included, goes only to `github.com`,
`objects.githubusercontent.com`, `release-assets.githubusercontent.com`, or the
exact host of `CASCADE_UPDATE_BASE_URL`. Each one passes the process egress
firewall as class `release-download` before it connects. At most 5 redirects
are followed. The firewall may not change a request URL; the one exception is
the signed asset query of a redirect to `objects.githubusercontent.com` or
`release-assets.githubusercontent.com`, which is requested as GitHub issued it.
Each request, with its redirects and its body, must finish within 10 minutes.
The `releases/latest` redirect must point at one of these hosts and name a tag
`v<semver>`. Proxy environment variables are not used. The release key is
never fetched over the network.
