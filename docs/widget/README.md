# macOS menu bar

The menu-bar application displays the daemon's widget snapshot. It uses native
AppKit views, a Unix-domain Network.framework connection, an SSE subscriber and
a 30-second silence poll. The socket path must be supplied in
`CASCADE_SOCKET_PATH`; an absent or relative path produces a single error line.
Labels are displayed verbatim. Unknown utilization, reset and job values remain
unknown and display an em-dash. A sign-in button opens a confirmation that shows
the command; confirming launches it in Terminal (see [reauth.md](reauth.md)).

This directory currently contains this overview only.

## Build and tests

Generate the Xcode project with xcodegen using `apps/widget-macos/project.yml`.
Build the `CascadeMenuBar` scheme on macOS 14 or later.
Supply the local signing team through an external xcconfig selected with
`XCODE_XCCONFIG_FILE`; no team identifier or signing identity value is tracked.
The project uses the installed Developer ID Application identity and hardened
runtime, without a sandbox or provisioning profile.

The `CascadeMenuBarTests` scheme builds the daemon and fixture seeder in its
pre-action. Its inherited PATH must select the required local Go toolchain and
provide the lock and timeout utilities. Dependencies must already be cached.
Both binaries use `CGO_ENABLED=0` so Xcode's deployment-target variables cannot
affect a C compiler invocation. Pre-action output is saved in
`$BUILT_PRODUCTS_DIR/widget-preaction.log` for build-failure diagnosis.
The integration test requires executable binary paths, checks that the temporary
HOME cannot access a default keychain, and runs the daemon with fresh HOME and
CASCADE_HOME directories. Missing binaries fail the test.
Keep the login session unlocked during the storage and integration tests:
complete file protection prevents their snapshot writes while the Mac is locked.

The fixtures in `CascadeMenuBarTests/testdata` are byte-for-byte copies of
`internal/daemon/testdata/fixture_status_widget_rpc.json` and
`internal/daemon/testdata/fixture_status_widget_changed_sse.txt`. The model test
compares the RPC fixture digest with its source when the source is available.
The SSE fixture retains its captured trailing blank line.
A separate client test checks the default seeder's exact five provider names.

## Snapshot privacy

RPC responses are sliced at the top-level `result` member; SSE snapshots are
base64-decoded. Neither path re-encodes the received snapshot. The writer stages
an empty file with mode 0600, requests complete file protection, verifies the
stored protection attribute, and only then writes bytes and atomically renames it to
`widget-snapshot.json`. A protection failure prevents publication and becomes a
single storage error in the popover. No fallback to weaker protection is used.
Any storage or fetch failure clears the in-memory snapshot and removes the
attention count from the menu-bar title. Failed publications invalidate the
previous file by removing it, or truncating it when directory permissions or a
delete-denying ACL prevent removal. A later successful snapshot restores the
normal display. Regression tests cover both an unwritable directory and a denied
atomic rename after a successful protected write.

## App Group spike, 2026-10-05

Two scratch application bundles were signed with the installed Developer ID
identity and the entitlement
`com.apple.security.application-groups = [<team>.com.acamarata.cascade.widget]`.
`codesign -d --entitlements :-` confirmed that expanded identifier in each
signature. Neither bundle contained `embedded.provisionprofile`. Both signed
executables wrote and read a temporary file in the group container successfully.
The files were removed by the probe. No registration or provisioning operation
was performed.

### Protection sequence

On the macOS 27.0.1 runner, the initial protection approaches failed:

- `createFile` with `NSFileProtectionComplete` succeeded but the persisted
  attribute was `NSFileProtectionCompleteUntilFirstUserAuthentication`.
- `Data.write` with `.completeFileProtection` failed with POSIX `EPERM`.
- `setAttributes` with `.complete` failed with POSIX `EPERM`.

These failures were reproduced in the App Group container using the approved
signature. Setting complete protection on an empty staging file before writing
the payload resolves the failure. The writer follows that sequence and verifies
the protection attribute before writing any snapshot bytes. The privacy test
then reads the final file and checks both its exact contents and persisted
complete-protection attribute. Successful signing and an ordinary container
write alone do not prove the snapshot-protection requirement.
