# Installing Cascade

Cascade ships as one static binary per platform. On macOS and Linux, `install.sh`
downloads a release, verifies it, and installs it into `~/.local/bin` without
sudo. On Windows, download and verify the zip by hand (see below).

## install.sh

```sh
curl -fsSLO https://github.com/acamarata/cascade/releases/latest/download/install.sh
sh install.sh            # latest stable release
sh install.sh v2.0.0     # a specific tag
```

Read the script before you run it. It is short, POSIX `sh`, and never asks for
root.

### What it does, in order

1. Maps `uname -s`/`uname -m` to a supported platform: `darwin/arm64`,
   `darwin/amd64`, `linux/amd64`, `linux/arm64`. Anything else exits 3 with
   the manual steps below. Nothing is written.
2. Fetches the release public key from the out-of-band location (see
   [Verifying a release by hand](#verifying-a-release-by-hand)), never from
   the release itself.
3. Downloads `cascade_checksums.txt` and `cascade_checksums.txt.minisig` for
   the tag and verifies the signature with `minisign`. `minisign` must be
   installed; the script refuses to run without it.
4. Picks this platform's archive from the verified checksums file: exactly
   one line whose file name matches
   `cascade_<version>_<os>_<arch>.tar.gz`. The name is never derived from the
   tag.
5. Downloads that archive and compares its sha256 with the verified line.
6. Lists the archive and refuses absolute paths, `..`, symlinks, hard links
   and special files. Then extracts it into a private temporary directory and
   checks again.
7. Installs each executable of the fixed set present at the archive root:
   `cascade-elevate-helper`, `cascade-github`, then `cascade` last. Each one is
   copied to `~/.local/bin/.<name>.new`, set to mode 0755 and renamed over
   `~/.local/bin/<name>`. Every other file in the archive is ignored.
8. Writes the install receipt (below).

Every download uses HTTPS only. A file whose sha256 already matches is left
untouched and reported as `already installed`, so running the same tag twice
changes nothing.

Make sure `~/.local/bin` is on your `PATH`.

### Environment

| Variable | Meaning |
|---|---|
| `CASCADE_INSTALL_BASE_URL` | Release download base (HTTPS). Default `https://github.com/acamarata/cascade/releases/download`. Files are fetched from `<base>/<tag>/`. |
| `CASCADE_MINISIGN_PUBKEY` | Path to a minisign public key file. Takes precedence over the URL. |
| `CASCADE_MINISIGN_PUBKEY_URL` | HTTPS URL of the public key. Default `https://acamarata.github.io/cascade/keys/minisign.pub`. |
| `CASCADE_INSTALL_DIST` | A local directory holding one `cascade_*_<os>_<arch>.tar.gz`, one `*_checksums.txt` and its `.minisig` (for example goreleaser's `dist/`). The tag argument then only labels the receipt. |
| `CASCADE_HOME` | Where the receipt is written. Default `$HOME/.cascade`. |

The script reads no other environment variables besides `HOME` and `PATH`.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Installed, or already installed. |
| 1 | Verification, download or write failure (bad or missing signature, unreachable or wrong key, checksum mismatch, unsafe archive, unwritable install dir, `minisign` missing). |
| 2 | `CASCADE_INSTALL_DIST` has no archive, several archives, several checksums files, or no signature. |
| 3 | Unsupported platform. |

Every non-zero exit happens before anything is installed or the receipt is
written.

### Install receipt

After every file is installed, the script writes
`${CASCADE_HOME:-$HOME/.cascade}/install-receipt.json` (mode 0644, via a temp
file and rename in the same directory):

```json
{"schema_version":1,"channel":"script","release_tag":"v2.0.0",
 "archive_name":"cascade_2.0.0_darwin_arm64.tar.gz","archive_sha256":"<hex>",
 "install_dir":"/Users/me/.local/bin","installed_at":"2026-01-01T00:00:00Z",
 "files":[{"name":"cascade","sha256":"<hex>"}]}
```

`release_tag` is always `v` plus the signed version, also for a
`CASCADE_INSTALL_DIST` run; self-update refuses a receipt without one.
`install_dir` is the absolute, symlink-free path. `files[]` lists only what
this script installed; self-update and uninstall act on those files alone and
check their digests first.

## Updating

`cascade self-update` updates an `install.sh` install in place:

```sh
cascade self-update --check   # report only
cascade self-update           # verify, swap, hand off the daemon
```

It verifies the signed checksums with the release key embedded in the binary,
refuses an older release (`downgrade`) and a release whose signed version does
not match its archive (`version mismatch`), and first checks every receipt file
against `install_dir` (`install differs from receipt`; rerun install.sh).

The previous files and receipt are kept in `${CASCADE_HOME:-$HOME/.cascade}/data/update/previous/`.
Exactly one generation is kept; the next update replaces it. If any step after
the swap fails, or a running daemon does not come back on the new version
within 30 seconds, the previous files are restored and the receipt is left as
it was. An interrupted swap (the process killed between two renames) is
refused by the next run with `install differs from receipt`: rerun `install.sh`;
the previous set stays in `${CASCADE_HOME:-$HOME/.cascade}/data/update/previous/`.
Only one self-update runs at a time.

`CASCADE_MINISIGN_PUBKEY`, `CASCADE_UPDATE_BASE_URL` and `CASCADE_UPDATE_DIST`
let whoever sets the environment choose the trust root and the release source.
Never run self-update under `sudo -E` or any privileged runner that keeps the
caller's environment.

Homebrew and OCI installs are updated by their own tool, and a node-managed
install by its controller; `self-update` says so and changes nothing. Details:
[`cli-reference/self-update.md`](cli-reference/self-update.md).

## Uninstalling

```sh
cascade uninstall --dry-run   # list every item, remove nothing
cascade uninstall             # print the plan, confirm, remove install items
cascade uninstall --purge-data   # also the data dir, config and elevation key (elevated)
```

The default run removes the service units, plugins (through their teardown
hooks), the vault entries Cascade indexed, the receipt-listed executables whose
sha256 still matches, and the receipt. Config and data stay. Backups are never
touched, with or without `--purge-data`. A file that changed since install is
kept and named, with the remedy: rerun `install.sh`, then `cascade uninstall`.
Homebrew installs keep the binary (`brew uninstall cascade`). Details:
[`cli-reference/uninstall.md`](cli-reference/uninstall.md).

## Verifying a release by hand

The release public key is published out of band, in two places:

- the repository: a copy of the key is added to the repository's key folder with the first signed release
- the project site: [acamarata.github.io/cascade/keys/minisign.pub](https://acamarata.github.io/cascade/keys/minisign.pub)

Fetch it from both and check they match. Then, for a release tag:

```sh
minisign -Vm cascade_checksums.txt -p minisign.pub
grep '_darwin_arm64.tar.gz$' cascade_checksums.txt   # your platform
shasum -a 256 cascade_<version>_darwin_arm64.tar.gz  # must equal that line
tar -tzvf cascade_<version>_darwin_arm64.tar.gz      # regular files only
```

Install minisign with `brew install minisign`, `apt install minisign`, or from
<https://jedisct1.github.io/minisign/>.

## Windows

`install.sh` does not support Windows. Download these files from the
[releases page](https://github.com/acamarata/cascade/releases):
`cascade_<version>_windows_amd64.zip`, `cascade_checksums.txt` and
`cascade_checksums.txt.minisig`. Then, in PowerShell:

```powershell
minisign -Vm cascade_checksums.txt -p minisign.pub
(Get-FileHash cascade_<version>_windows_amd64.zip -Algorithm SHA256).Hash.ToLower()
Select-String '_windows_amd64.zip$' cascade_checksums.txt
```

The two digests must match. Extract the zip and put `cascade.exe` in a
directory on your `PATH`. minisign for Windows is on its releases page. winget
and scoop packages are not offered.
