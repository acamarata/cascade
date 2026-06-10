# Code Signing

Cascade signs its binaries and packages to protect users from tampered downloads and to satisfy OS security gates on macOS, Windows, and Linux.

---

## Why signing matters

On macOS, any unsigned application triggers Gatekeeper, which blocks launch or shows a scary warning. On Windows, SmartScreen flags unsigned executables. Signed binaries skip these prompts and assure users the binary they downloaded matches what the project published.

Signing also lets you verify integrity yourself using the tools below.

---

## macOS

The macOS `.app` bundle and the `cascade` CLI binary are both signed and notarized.

**What is signed:**
- `Cascade.app` (Tauri 2 bundle)
- `cascade` CLI binary inside the app bundle
- The standalone `cascade` CLI binary distributed via Homebrew and direct download

**Certificate:** Apple Developer ID Application certificate, issued to the `acamarata` Apple Developer account.

**Notarization:** The app is submitted to Apple's notarization service after signing. Notarization checks the binary for known malware and signs Apple's approval into a stapled ticket on the bundle.

**CI signing script:** `.github/workflows/sign-macos.sh` — imports the cert, runs `codesign --deep --force --options runtime`, verifies with `codesign --verify --deep --strict`, submits to `xcrun notarytool`, and staples the ticket.

**Secrets required:** `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_SPECIFIC_PASSWORD` (set via `gh secret set` — see [`.github/docs/code-signing.md`](../docs/code-signing.md#macos--apple-developer-id--notarization)).

**Gatekeeper verification:**

```bash
codesign --verify --deep --strict Cascade.app
spctl --assess --verbose Cascade.app
```

Both commands should return `accepted` or `satisfies its Designated Requirement`.

---

## Windows

The Windows installer (`.msi`) is signed with an Authenticode certificate via [SignPath.io](https://about.signpath.io/product/foss) (free FOSS tier).

**What is signed:**
- `Cascade_*.msi` (Tauri 2 Windows installer)

**CI signing workflow:** `.github/workflows/sign-windows.yml` — reusable workflow called from `release.yml`; uses `signpath/github-action-submit-signing-request@v1.1`. Artifact configuration slug: `cascade-msi`. See also `.github/signpath-policy.yml`.

**Secrets required:** `SIGNPATH_API_TOKEN`, `SIGNPATH_ORGANIZATION_ID` (set via `gh secret set`). Enrollment steps: [`.github/docs/code-signing.md`](../docs/code-signing.md#windows--authenticode-via-signpathio-foss).

**Verification:**

Right-click the file, select Properties, and check the Digital Signatures tab. Or verify from PowerShell:

```powershell
Get-AuthenticodeSignature cascade-setup.msi
```

Expected output: `SignerCertificate` shows the acamarata certificate, `Status` shows `Valid`.

---

## Linux

Linux does not have a universal signing gate equivalent to Gatekeeper or SmartScreen. Cascade provides GPG detached signatures and SHA256 checksums for each release artifact.

**What is signed:** Every Linux `.tar.gz`, `.deb`, and `.rpm` artifact gets a detached `.asc` signature. The `checksums.txt` file is also GPG-signed.

**CI signing step:** Wired in `release.yml` — imports `GPG_PRIVATE_KEY`, loops over `*.tar.gz *.deb *.rpm`, produces `.asc` sidecars with `gpg --detach-sign --armor`.

**Secrets required:** `GPG_PRIVATE_KEY` (ASCII-armored private key), `GPG_PASSPHRASE`. See [`.github/docs/code-signing.md`](../docs/code-signing.md#linux--gpg-artifact-signatures).

**Release key fingerprint:** `3C463D90DF3061AA752FB8500F5729773E694CEA` (also in `SECURITY.md` and `.github/RELEASE_KEY.asc`).

**Verify an artifact:**

```bash
gpg --import https://github.com/acamarata/cascade/raw/main/.github/RELEASE_KEY.asc
gpg --verify cascade-x86_64-unknown-linux-gnu.tar.gz.asc cascade-x86_64-unknown-linux-gnu.tar.gz
```

**Checksum file:** Every release attaches a `checksums.txt` to the GitHub release. Verify:

```bash
sha256sum -c checksums.txt
gpg --verify checksums.txt.asc checksums.txt
```

**Package manager trust:**

- AUR: the `cascade-bin` PKGBUILD includes the SHA256 hash of the release tarball, verified by makepkg.
- Snap: snap packages are signed by Canonical's infrastructure.
- Flatpak: packages are signed by the Flathub signing key.

---

## crates.io

The `cascade-cli` crate on crates.io is published from the GitHub Actions CI pipeline. The workflow has access to the crates.io token only within the Actions runner. Source code is open and auditable.

---

## Reproducible builds

The project works toward reproducible builds. For CLI binaries built with `cargo`, the same source at the same commit produces the same output when built with the same Rust toolchain. You can verify by building from source and comparing checksums:

```bash
cargo build --release -p cascade-cli
sha256sum target/release/cascade
```

Compare against the checksum in the release's `checksums.txt`.

See also: [Building From Source](Building-From-Source.md) · [Security](Security.md)
