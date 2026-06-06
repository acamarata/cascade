/**
 * Purpose: Typed error class for Cascade IPC failures surfaced to the frontend.
 * Inputs:  A string error code and a human-readable message from Tauri invoke rejection.
 * Outputs: CascadeIpcError instance — extends Error with a programmatic `code` field.
 * Constraints: The `code` field maps to CascadeError variants serialised by the Rust
 *   Tauri command layer (e.g. "DaemonNotRunning", "IpcTimeout", "NotImplemented").
 *   Never swallow the code — callers depend on it for branch logic.
 * SPORT: MASTER-LIBS.md — errors.ts, src/types/errors.ts
 */

/**
 * Thrown by every ipc.* method when the Tauri invoke() call rejects.
 *
 * The `code` field is the Rust CascadeError variant name, serialised as a string
 * by the Tauri command error serialiser. Use it for programmatic error handling
 * (retry logic, daemon-not-running UI, etc.).
 *
 * @example
 * ```ts
 * try {
 *   const status = await ipc.getStatus();
 * } catch (e) {
 *   if (e instanceof CascadeIpcError && e.code === 'DaemonNotRunning') {
 *     // show "start daemon" prompt
 *   }
 * }
 * ```
 */
export class CascadeIpcError extends Error {
  /**
   * Machine-readable error code from the Rust CascadeError serialisation.
   * Examples: "DaemonNotRunning", "IpcTimeout", "NotImplemented", "IpcError".
   */
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'CascadeIpcError'
    this.code = code
  }
}
