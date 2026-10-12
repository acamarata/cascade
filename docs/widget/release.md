# Widget release: build, sign, notarize

How the macOS widget (`apps/widget-macos`) becomes a signed, notarized,
stapled `CascadeMenuBar.zip` and `CascadeMenuBar.dmg`. Two scripts do the
work and write only into `$CASCADE_WIDGET_OUT`. Neither publishes anything,
uploads anything except the notary submission, or writes to a keychain.

| File | Owns |
|---|---|
| `apps/widget-macos/ExportOptions.plist` | Developer ID export options: `method` developer-id, `signingStyle` manual, `stripSwiftSymbols`. It never names a certificate or team. |
| `.github/scripts/build-widget-release.sh` | Generates the Xcode project, archives the `CascadeMenuBar` scheme and leaves `$CASCADE_WIDGET_OUT/CascadeMenuBar.app`. |
| `.github/scripts/sign-notarize-widget.sh` | `submit` and `finish`: signing, notarization, stapling, packaging and checksums. |
| `internal/build/widget_release_test.go` | Drives both scripts through PATH shims; no test touches Xcode, a keychain or Apple. |

## Environment

| Variable | Used by | Meaning |
|---|---|---|
| `CASCADE_WIDGET_OUT` | both | Output directory (required). Inside the repository it must be a gitignored path. |
| `CASCADE_APPLE_SIGNING_IDENTITY` | both | Codesign identity. `-` means ad-hoc. |
| `CASCADE_APPLE_TEAM_ID` | build | Team id (ten letters or digits); required unless the identity is `-`. |
| `CASCADE_APPLE_NOTARY_KEY_ID`, `CASCADE_APPLE_NOTARY_KEY_ISSUER`, `CASCADE_APPLE_NOTARY_KEY_PATH` | sign | App Store Connect API key id, issuer and `.p8` file. |
| `CASCADE_NOTARY_SKIP` | sign | `1` skips signing and notarization. |

The build script never reads the notary variables. The sign script passes
them only to `notarytool` and never prints them. It never prints `notarytool`
output raw either: on a failure it prints `[line withheld]` for every line
that contains a line of the key file, a base64 run of 40 or more characters
or `PRIVATE KEY`, and replaces the key id and issuer in any letter case. If
the key file cannot be read, it withholds the whole output. The key id and
issuer are on `notarytool`'s command line for the length of each call, so
other local users can see them with `ps`.
Apple does not treat them as secret; the `.p8` file is. Keep it outside the
repository and remove it after each call.

## Build

```sh
CASCADE_WIDGET_OUT=$OUT CASCADE_APPLE_SIGNING_IDENTITY=... CASCADE_APPLE_TEAM_ID=... \
  bash .github/scripts/build-widget-release.sh
```

The script copies `apps/widget-macos` to `$OUT/.widget-build/src`, runs
`xcodegen` there and archives into `$OUT/.widget-build`, so the working tree
is unchanged. It first removes the packages, `notarization.txt` and their
`checksums.txt` lines from any earlier run.

- Identity `-`: the archived `.app` is copied out. There is no export.
- Any other identity: `ExportOptions.plist` is copied to
  `$OUT/ExportOptions.rendered.plist`, `signingCertificate` and `teamID` are
  added there with PlistBuddy, and `xcodebuild -exportArchive` uses it.

## Sign and notarize

`submit` journals `begin`, removes the previous run's packages and marker,
signs the extension and the app with the hardened runtime and a secure
timestamp, verifies the signature, zips the app, journals an `intent` line,
runs `notarytool submit` without `--wait` and journals `submitted id=<id>`.

`finish` polls once with `notarytool info` for the id confirmed after the
last `begin`, `intent` or `skip` line, and journals the status. A rebuild
journals `begin build`, so an id never outlives the app it was issued for.

| Status | `finish` does | Exit |
|---|---|---|
| Accepted | staples and validates the app, verifies its signature, writes the `.zip` and a signed `.dmg`, replaces their lines in `checksums.txt`, writes `notarization.txt` `accepted id=<id>` | 0 |
| In Progress | nothing; call `finish` again | 75 |
| Invalid, Rejected | fetches `notary-log.json`, adds nothing | 1 |
| anything else | adds nothing | 1 |

If `submit` fails or dies at any point before it journals an id, `finish`
refuses to poll, staple or package until a new `submit` confirms one. The
packages are built in `$OUT/.pkg` and moved into place only after the
`.dmg` is signed. The `.dmg` itself is signed, not notarized: the app inside
it carries the notarization ticket.

`$OUT/notary-journal.log` holds timestamps, the submitted zip's sha256, the
submission id and each status. It never holds a credential.

## Ad-hoc snapshot

```sh
OUT=$(mktemp -d)
export CASCADE_APPLE_SIGNING_IDENTITY=- CASCADE_NOTARY_SKIP=1 CASCADE_WIDGET_OUT=$OUT
bash .github/scripts/build-widget-release.sh
bash .github/scripts/sign-notarize-widget.sh submit
bash .github/scripts/sign-notarize-widget.sh finish
(cd "$OUT" && shasum -a 256 -c checksums.txt)
```

Skip mode writes `notarization.txt` with `skipped`. A skipped build is never
a release; release checks reject that marker.

## Verifying a notarized build

```sh
A=$OUT/CascadeMenuBar.app
codesign --verify --deep --strict "$A"
spctl -a -vv "$A"            # accepted, source=Notarized Developer ID
xcrun stapler validate "$A"
codesign -d --entitlements :- "$A"
```

The signing identity must already be in the build host's keychain, and that
keychain must be unlocked for the session running the scripts. The scripts
never import, create or unlock a keychain.
