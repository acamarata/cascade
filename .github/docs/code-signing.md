# Code Signing — Setup Guide

This document covers the one-time setup steps required to enable code signing for each platform. These steps require external accounts and must be performed by the repository owner. All CI configuration files are already in place; only the secrets and account enrollments listed below are needed.

---

## Overview

| Platform | Method | Secret(s) required | Status |
|---|---|---|---|
| macOS | Apple Developer ID + notarytool | `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_SPECIFIC_PASSWORD` | Infrastructure done — USER-AUTH pending |
| Windows | Authenticode via SignPath.io FOSS | `SIGNPATH_API_TOKEN`, `SIGNPATH_ORGANIZATION_ID` | Infrastructure done — USER-AUTH pending |
| Linux | GPG detached signatures | `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE` | Key in SECURITY.md — wire secrets |

All signing steps in `release.yml` are gated: when a secret is absent the step logs a message and skips gracefully. Open-source forks that have not enrolled produce unsigned artifacts without CI failure.

---

## macOS — Apple Developer ID + Notarization

### What the CI does (already configured)

1. `.github/workflows/sign-macos.sh` imports your Developer ID cert into a temporary keychain.
2. Runs `codesign --deep --force --options runtime` with the entitlements from `apps/cascade-app/src-tauri/entitlements.plist`.
3. Verifies the signature with `codesign --verify --deep --strict`.
4. Submits the DMG to `xcrun notarytool` and waits for Apple approval.
5. Staples the notarization ticket with `xcrun stapler staple`.

### User actions required

**Step 1 — Enroll in Apple Developer Program**

Go to https://developer.apple.com/programs/ and enroll in the Apple Developer Program ($99/year). You need an Apple ID and a payment method.

**Step 2 — Create a Developer ID Application certificate**

1. Open Xcode or go to https://developer.apple.com/account/resources/certificates/list.
2. Click the `+` button, select "Developer ID Application", and follow the prompts.
3. Download the `.cer` file and double-click to add it to Keychain Access.
4. Export the certificate from Keychain Access as a `.p12` file (right-click the cert → Export). Set a strong passphrase.

**Step 3 — Generate an app-specific password**

1. Go to https://appleid.apple.com/account/manage.
2. Under Security, click "Generate Password" in the App-Specific Passwords section.
3. Label it "cascade-notarytool" and copy the password.

**Step 4 — Find your Team ID**

Go to https://developer.apple.com/account and find your Team ID in the top-right corner (10-character alphanumeric string).

**Step 5 — Set GitHub repository secrets**

Run the following commands from the `acamarata/cascade` repository root, substituting actual values:

```bash
# Base64-encode the .p12 certificate file
CERT_B64=$(base64 -i ~/path/to/developer-id-application.p12)

gh secret set APPLE_CERTIFICATE --body "$CERT_B64"
gh secret set APPLE_CERTIFICATE_PASSWORD --body "your-p12-passphrase"
gh secret set APPLE_ID --body "your-apple-id@example.com"
gh secret set APPLE_TEAM_ID --body "XXXXXXXXXX"
gh secret set APPLE_APP_SPECIFIC_PASSWORD --body "xxxx-xxxx-xxxx-xxxx"
```

**Step 6 — Verify**

Push a new tag (`git tag v0.1.0 && git push --tags`). The "Sign macOS app" step in release.yml should run and complete without error. Check the GitHub Actions run log for "Notarization complete — ticket stapled".

### Entitlements reference

The entitlements file is at `apps/cascade-app/src-tauri/entitlements.plist`. It grants:

| Entitlement | Value | Why |
|---|---|---|
| `com.apple.security.app-sandbox` | `false` | Required for daemon IPC socket (cross-process Unix domain socket) |
| `com.apple.security.network.client` | `true` | AI provider HTTP connections, MCP transports |
| `com.apple.security.files.user-selected.read-write` | `true` | File-picker dialogs for context attachment and RAG ingestion |

---

## Windows — Authenticode via SignPath.io FOSS

### What the CI does (already configured)

`release.yml` calls `.github/workflows/sign-windows.yml`, which uses the `signpath/github-action-submit-signing-request@v1.1` action to:

1. Upload the MSI artifact to SignPath.io.
2. Wait for the signing policy approval (automatic for FOSS projects with auto-approve enabled).
3. Download the signed MSI back and upload it as a release artifact.

The signing step is skipped when `SIGNPATH_API_TOKEN` is absent.

The artifact configuration spec is in `.github/signpath-policy.yml`. The artifact slug is `cascade-msi`; the signing policy slug is `release-signing`.

### User actions required

**Step 1 — Create a SignPath account**

Go to https://app.signpath.io and create a free account.

**Step 2 — Enroll in the FOSS program**

SignPath offers free code signing for open-source projects. Apply at:
https://about.signpath.io/product/foss

Requirements: the repository must be public, licensed under an OSI-approved license (MIT qualifies), and have the repository URL registered in SignPath. The review takes 1-5 business days.

**Step 3 — Create an organization and project**

1. In SignPath, create an organization named "acamarata" (or your preferred name).
2. Inside the organization, create a project named "cascade".
3. Note your Organization ID (UUID shown in the org settings).

**Step 4 — Create an artifact configuration**

1. Go to Projects → cascade → Artifact Configurations → New.
2. Set slug: `cascade-msi`
3. Set file pattern: `*.msi`
4. Set signing method: Authenticode
5. Save and publish.

**Step 5 — Create a signing policy**

1. Go to Projects → cascade → Signing Policies → New.
2. Set slug: `release-signing`
3. Set approver: yourself (or enable auto-approve for the FOSS tier).
4. Link it to the `cascade-msi` artifact configuration.
5. Save and publish.

**Step 6 — Generate an API token**

1. Go to your SignPath user settings → CI User Tokens.
2. Create a new token.
3. Copy the token value (shown only once).

**Step 7 — Set GitHub repository secrets**

```bash
gh secret set SIGNPATH_API_TOKEN --body "your-signpath-api-token"
gh secret set SIGNPATH_ORGANIZATION_ID --body "your-org-uuid"

# Enable the conditional gate (repository variable, not secret)
gh variable set SIGNPATH_ENABLED --body "true"
```

**Step 8 — Verify**

Push a new tag. The "Sign Windows MSI (Authenticode)" job in release.yml should run. Check the SignPath dashboard to confirm the signing request was received and completed.

---

## Linux — GPG Artifact Signatures

### What the CI does (already configured)

For each Linux build matrix entry in `release.yml`, after the artifact is built:

1. Imports the GPG private key from the `GPG_PRIVATE_KEY` secret.
2. Signs each `*.tar.gz`, `*.deb`, and `*.rpm` with `gpg --detach-sign --armor`, producing a `.asc` sidecar for each artifact.
3. The `publish` job uploads both the artifact and the `.asc` to the GitHub Release.
4. The `checksums.txt` file (SHA256 of all artifacts) is also signed with the same key.

The signing step is skipped when `GPG_PRIVATE_KEY` is absent.

The release key fingerprint is `3C463D90DF3061AA752FB8500F5729773E694CEA`, documented in `SECURITY.md` and published at `.github/RELEASE_KEY.asc`.

### User actions required

The GPG key was created in T-P4-E05-05. To wire it into CI:

**Step 1 — Export the private key**

On the machine where the key was generated:

```bash
# Export the private key as ASCII armor
gpg --armor --export-secret-keys 3C463D90DF3061AA752FB8500F5729773E694CEA > cascade-release-private.asc
```

**Step 2 — Set GitHub repository secrets**

```bash
gh secret set GPG_PRIVATE_KEY < cascade-release-private.asc
gh secret set GPG_PASSPHRASE --body "the-key-passphrase"
```

Then securely delete the exported private key file from disk:

```bash
shred -u cascade-release-private.asc
```

**Step 3 — Verify**

Push a new tag. Check the GitHub Release page — each Linux `.tar.gz` should have a corresponding `.tar.gz.asc` attachment, and `checksums.txt.asc` should be present.

---

## Verifying signatures (user-facing)

See `Distribution-Channels.md § Verifying signatures` for the commands users run to verify downloaded artifacts.

```bash
# Verify GPG signature on a Linux artifact
gpg --import https://github.com/acamarata/cascade/raw/main/.github/RELEASE_KEY.asc
gpg --verify cascade-x86_64-unknown-linux-gnu.tar.gz.asc cascade-x86_64-unknown-linux-gnu.tar.gz

# Verify macOS code signature
codesign --verify --deep --strict Cascade.app
spctl --assess --verbose Cascade.app

# Verify Windows Authenticode
Get-AuthenticodeSignature cascade-setup.msi
```

---

## Secret inventory

All secrets referenced by release.yml:

| Secret name | Platform | Source |
|---|---|---|
| `APPLE_CERTIFICATE` | macOS | base64-encoded .p12 Developer ID Application cert |
| `APPLE_CERTIFICATE_PASSWORD` | macOS | .p12 export passphrase |
| `APPLE_ID` | macOS | Apple ID email address |
| `APPLE_TEAM_ID` | macOS | 10-char Team ID from developer.apple.com |
| `APPLE_APP_SPECIFIC_PASSWORD` | macOS | App-specific password from appleid.apple.com |
| `SIGNPATH_API_TOKEN` | Windows | SignPath CI User Token |
| `SIGNPATH_ORGANIZATION_ID` | Windows | SignPath organization UUID |
| `GPG_PRIVATE_KEY` | Linux | ASCII-armored GPG private key |
| `GPG_PASSPHRASE` | Linux | GPG key passphrase |

Repository variable (not a secret):

| Variable name | Value | Purpose |
|---|---|---|
| `SIGNPATH_ENABLED` | `true` | Enables the SignPath job gate |

See also: [Code-Signing wiki page](../wiki/Code-Signing.md) · [Security](../wiki/Security.md) · [SECURITY.md](../../SECURITY.md)
