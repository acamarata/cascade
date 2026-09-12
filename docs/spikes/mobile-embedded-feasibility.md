# Feasibility Spike: Mobile & Embedded Library-Build (P1-E28-W10-S58-T5)

**Recommendation**: FEASIBLE-WITH-MAJOR-WORK (primary blocker: gomobile/c-archive toolchain CGO requirements fundamentally conflict with Cascade's core no-CGO rule, compounded by pervasive desktop-process assumptions).

## Findings

### 1. Mobile & Embedded Library Toolchains (gomobile in 2026)
Compiling Go as an embeddable mobile library target (iOS XCFramework or Android AAR) requires `gomobile bind` or `go build -buildmode=c-archive`. In 2026, gomobile remains in maintenance-only status within `golang.org/x/mobile`. Its binding generator (`gobind`) supports only primitive types, `[]byte`, `string`, `error`, and simple interfaces; it does not support `context.Context`, generics, maps, or arbitrary slices. Furthermore, gomobile and `c-archive` require `CGO_ENABLED=1` to compile platform C bridges and link the Go runtime into a native library. For embedded Linux, cross-compiling a standalone static binary (`CGO_ENABLED=0`) is natively supported, but building a C-shared library (`-buildmode=c-shared`) requires an external C cross-compiler and CGO.

### 2. Codebase Blockers
1. **CGO vs. Core Policy Conflict**: Cascade mandates `CGO_ENABLED=0` for core builds. Attempting `CGO_ENABLED=1` fails for `GOOS=android` or `GOOS=ios` because platform-specific files lack build tags for mobile OSes (`android` and `ios` are distinct from `linux` and `darwin` in Go).
2. **Subprocess (`os/exec`) Dependence**: Mobile sandboxes (iOS App Store containers and Android app sandboxes) forbid spawning external processes via `fork`/`exec`. Cascade relies on external binaries across multiple subsystems: macOS Keychain access executes `/usr/bin/security`, external plugins spawn processes via `exec.CommandContext`, and repository indexing invokes `git`.
3. **Unix Socket and Daemon IPC**: The daemon architecture assumes IPC over a Unix domain socket serving HTTP JSON-RPC. In an in-process mobile library, Unix socket IPC creates filesystem permission friction in mobile sandboxes and introduces unnecessary serialization overhead instead of direct native bindings (JNI/Swift).
4. **SQLite Driver and File Locking**: The `modernc.org/sqlite` driver is pure Go but lacks sidecar file lock implementations for `android` and `ios`. Additionally, modernc transpiles C to Go, adding substantial binary and heap overhead, whereas iOS and Android bundle system SQLite libraries.
5. **Filesystem Path Conventions**: Runtime configuration defaults to desktop conventions (`~/.cascade` via `os.UserHomeDir`). Mobile applications require paths strictly anchored inside container sandboxes (`Context.getFilesDir()` on Android, `NSApplicationSupportDirectory` on iOS).
6. **Lifecycle and POSIX Signals**: The daemon registers global handlers for `SIGTERM`, `SIGINT`, and `SIGHUP`, and runs continuous event loops. In mobile environments, global POSIX signal handlers interfere with host application crash reporters, and unpaused background worker goroutines cause watchdog termination (iOS jetsam).

### 3. Footprint Implications
A baseline Go mobile library starts at 10–15 MB uncompressed. Dependencies such as `modernc.org/sqlite` and `github.com/tetratelabs/wazero` add 20–30 MB per architecture slice, exceeding 60–80 MB for a multi-architecture iOS XCFramework. Memory-wise, Go's GC allocator arenas and modernc's memory model risk triggering aggressive mobile memory killers (iOS app extensions are limited to 30 MB RSS). Wazero also cannot use its JIT compiler on iOS due to W^X page restrictions, forcing interpreted mode.

### 4. Realistically Embeddable Subset
- **Shippable**: `pkg/cascade` (types, errors), `pkg/provider` (abstract interfaces), `internal/events` (in-memory bus), and pure HTTP model providers (`providers/openai`, `providers/anthropic`, `providers/gemini`).
- **Must Be Cut**: `cmd/cascade`, `internal/daemon`, `internal/plugins/process`, `internal/retrieval/gitcorpus`, and desktop elevation.
- **Must Be Abstracted**: Storage (`provider.Store`) backed by host native SQLite; `internal/secrets.Custody` delegated to native platform keychains via host callbacks; `runtime.PathProvider` injected with host container paths; and daemon lifecycle replaced with explicit `Start`/`Pause`/`Resume`/`Stop` methods. A dedicated mobile facade package with `gobind`-compatible signatures is required.

## Evidence

- `internal/elevation/keystore_darwin.go:1,161`: CGO import and `darwin && cgo` constraint (excludes `ios`).
- `internal/elevation/keystore_linux.go:1,109`: PAM CGO import and `linux && cgo && pam` constraint (excludes `android`).
- `internal/elevation/keystore_darwin_nocgo.go:1`: Fallback restricted to `darwin && !cgo`.
- `internal/secrets/custody_darwin.go:1-5,30`: Executes `/usr/bin/security` via `os/exec`.
- `internal/plugins/process/runtime.go:32,58-59`: Spawns plugin subprocesses via `exec.CommandContext`.
- `internal/retrieval/gitcorpus.go:54`: Imports `os/exec` to invoke external `git`.
- `internal/daemon/lifecycle_unix.go:60-62,237,262`: Traps `SIGTERM`/`SIGINT` and binds/dials Unix socket.
- `internal/runtime/hotreload_signal.go:1,21-25`: Traps `syscall.SIGHUP` on `!windows`.
- `internal/runtime/lockfile_unix.go:1,43`: Build tag `darwin || linux` calling `unix.Kill` (missing on `ios`/`android`).
- `providers/sqlite/driver.go:1-12,53`: `modernc.org/sqlite` pure-Go driver registration.
- `providers/sqlite/flock_darwin.go:1`: File locking restricted to `darwin` (missing on `ios`).
- `providers/sqlite/flock_linux.go:1`: File locking restricted to `linux` (missing on `android`).
- `internal/runtime/paths.go:33-47,69-75`: Hardcoded desktop fallback to `os.UserHomeDir` and `~/.cascade`.
- `internal/daemon/context_assemble.go:228`: Direct call to `os.UserHomeDir`.
- `go.mod:19,26,58-60`: Heavyweight dependencies `modernc.org/sqlite` and `github.com/tetratelabs/wazero`.
- `internal/build/desktop_test.go:26-48,148-150`: Depguard arch tests enforcing headless non-GUI imports.
