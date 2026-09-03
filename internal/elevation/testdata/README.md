# internal/elevation integration-test provenance

Art.2 real-counterpart record for the two build-tagged integration tests in
this package: `keystore_darwin_integration_test.go`
(`//go:build darwin && cgo && integration`) and
`keystore_linux_integration_test.go` (`//go:build linux && cgo && integration`).

## darwin

- Built and (partially) compiled against: macOS 26.6.2, build 25G83
  (`sw_vers`), Apple clang version 21.0.0 (clang-2100.1.1.101),
  arm64-apple-darwin25.6.0.
- Security.framework / LocalAuthentication.framework: the versions shipped
  with macOS 26.6.2 on this host (no separate framework version string is
  exposed by either framework; they are tied to the OS build above).
- Date: 2026-09-03.
- What was actually run in this ticket's session: `go build`, `go vet`, and
  the non-integration unit test suite (`keystore_test.go`,
  `trust_test.go`) — all real CGO/Objective-C compiled and linked
  successfully against the real frameworks on this host.
- What was **NOT** run: `TestDarwinKeystore_RealSecurityFramework_NonInteractive`
  itself. It is gated behind `CI_SKIP_BIOMETRICS=1` AND
  `CASCADE_INTEGRATION_WRITE_KEYCHAIN=1` specifically so an unattended
  session cannot accidentally write a real item into a developer's login
  Keychain. This ticket's session never set either variable, so the test
  was written and reviewed for correctness but never executed. The
  `Sign()`/`LAContext.evaluatePolicy` path — the actual biometric/passcode
  prompt — is untested by ANY automation in this repository as of this
  ticket: it requires a human physically present to answer a system
  prompt, which no CI runner and no unattended agent session can provide.
  A real verification of that path is a manual, one-time task for whoever
  next has hands on a macOS box with Touch ID or a passcode configured.

## linux

- No linux machine, container, or cross-compiler toolchain was available in
  this ticket's session (darwin-only sandbox). `keystore_linux.go` and
  `keystore_linux_integration_test.go` were written by careful inspection
  against the documented libpam C API (`pam_start`, `pam_authenticate`,
  `pam_acct_mgmt`, `pam_end`) and the stable `add_key(2)`/`keyctl(2)`
  syscalls, cross-checked against `GOOS=linux go vet` where that tool's
  type-checking could run without CGO (i.e. the non-cgo fallback file
  only — `keystore_linux_nocgo.go`, confirmed to build). The `cgo`-tagged
  file itself was never compiled anywhere in this session.
- Expected CI fixture: a `pam_permit.so`-backed PAM service file at
  `/etc/pam.d/cascade-elevate` (falls back to `/etc/pam.d/other` if that
  stacks `pam_permit.so`), so `pam_authenticate` succeeds without a real
  credential prompt. `CI_HAS_PAM_PERMIT=1` must be set to opt into running
  the test against that fixture; libpam version and distro should be
  recorded here by whoever first runs it for real.
- This is the honest Art.12 risk-spike answer for the linux leg: the code
  is real (not a stub) but genuinely UNVERIFIED in this session. CI's own
  linux runner is this code's first real compilation and execution.
