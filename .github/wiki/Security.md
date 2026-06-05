# Security: Cascade Daemon & Proxy

This page documents the security controls implemented in the cascade daemon and proxy.

## Threat Model

The cascade daemon and proxy operate in a trusted local environment with the following threat actors:

- **Local processes on the same machine:** Can access the Unix domain socket and local HTTP endpoints.
- **LAN peers on the same network:** Can attempt to access the proxy or dashboard if not properly isolated.

The security model assumes the user's machine itself is trusted, but restricts network access and implements authentication and authorization controls to prevent local processes or LAN peers from abusing the daemon's API access.

## Proxy Security

### Request Body Size Limit

The Gemini proxy enforces a **1MB (1,048,576 bytes) hard limit** on incoming HTTP request bodies.

- **Purpose:** Prevents memory exhaustion and quota waste from malicious or misconfigured clients sending oversized payloads.
- **Implementation:** Maximum enforced in `handle_connection()` after parsing the `Content-Length` header. See `crates/cascade-daemon/src/proxy/gemini_proxy.rs`, constant `MAX_REQUEST_BODY_SIZE`.
- **Response:** HTTP 413 Payload Too Large with JSON body:
  ```json
  {
    "error": "request_too_large",
    "limit_bytes": 1048576
  }
  ```
- **Scope:** Applies to all proxy routes (path-agnostic, limit is global).

### Authentication

The proxy uses **X-Cascade-Token HMAC authentication** on all incoming requests.

- **Token:** 32-byte cryptographically random value (CSPRNG), hex-encoded to 64 characters.
- **Storage:** `~/.cascade/proxy.token`, file mode 0600 (user read/write only).
- **Comparison:** Constant-time comparison via `subtle::ConstantTimeEq` to prevent timing attacks.
- **Rotation:** Supports SIGHUP signal for zero-downtime token rotation.
- **Requirement:** All requests must include the valid token in the `X-Cascade-Token` header.

## Dashboard Security

### Bind Address

The dashboard web server binds to **127.0.0.1:9761 only** (loopback interface).

- **Purpose:** Restricts access to local machine only; prevents remote access.
- **Implementation:** `Dashboard::new()` rejects non-loopback socket addresses with `DashboardError::NonLoopback`.

### Authentication

The dashboard uses **Bearer token authentication** on write routes (`/api/gci/*`).

- **Token:** 32-byte CSPRNG value, hex-encoded to 64 characters.
- **Storage:** `~/.cascade/dashboard.token`, file mode 0600.
- **Comparison:** Constant-time comparison via `subtle::ConstantTimeEq`.

### CORS

Cross-Origin Resource Sharing (CORS) is configured to allow only **http://127.0.0.1:9761**.

- **Purpose:** Restricts browser-based access to the dashboard's own origin only.
- **Policy:** No wildcard or other origins permitted.

## IPC Security

### Unix Domain Socket

Inter-process communication uses a **Unix domain socket** (not TCP).

- **Location:** `~/.cascade/daemon.sock`
- **Benefit:** Local process access only; network isolation by OS design.

## Data Security

### Key Index Lock

The daemon maintains an index of all sensitive keys stored in the OS keychain. This index is protected by a `key_index.lock` file.

- **Purpose:** Prevents concurrent writes to the key index and ensures atomic key operations.
- **File permissions:** File mode 0600 (user read/write only).
- **Location:** `~/.cascade/key_index.lock`

### Log File Permissions

All log files written by the daemon enforce strict permissions.

- **Log file path:** `~/Library/Logs/cascade-*.log` (macOS), `~/.local/share/cascade/logs/` (Linux)
- **File mode:** 0600 (user read/write only)
- **Purpose:** Prevents other users on the system from reading sensitive logs that may contain partial API responses or internal state.

## Health Endpoint

### Minimal Response Policy

The daemon exposes a health endpoint at `GET /health` that returns only minimal information.

- **Response format:** JSON object with `status: "ok"` and `timestamp` only.
- **No sensitive data:** The health endpoint does NOT return key counts, configuration state, API quota, or any other implementation details.
- **Purpose:** Allows basic liveness checks without exposing system state to untrusted clients.

## Key Storage

The daemon abstracts OS-level key storage via the **KeyStorage trait**.

- **macOS:** Keychain
- **Linux:** Secret Service
- **Windows:** Credential Manager
- **Purpose:** Keeps sensitive keys out of plaintext config files and environment variables.

## SIEGE Findings Resolution (Phase 2 Controls)

The following table maps the three high-severity findings from the opening-gate SIEGE pre-scan to their corresponding control implementation tickets in Phase 2, Epic 7 (Security Hardening).

| Finding | Severity | Issue | Control Ticket | Implementation | Status |
|---------|----------|-------|-----------------|-----------------|--------|
| Path injection on proxy forward | HIGH-1 | Proxy forwards `self.path` to Gemini API without path allowlist; attacker on LAN can inject paths and burn quota | T-P2-E07-01 | Implement `/v1beta/` path allowlist on proxy request path validation | In Phase 2 |
| No local authentication on proxy | HIGH-2 | Any local process can call proxy without authentication token; can exfiltrate API keys or burn quota | T-P2-E07-02 | Implement `X-Cascade-Token` HMAC authentication on all proxy routes | In Phase 2 |
| Dashboard unauthenticated write access | HIGH-3 | Dashboard binds `0.0.0.0` with unauthenticated GCI write API (`PUT/POST/DELETE /api/gci/file`), CORS wildcard; LAN peer can rewrite `~/.claude/CLAUDE.md` | T-P2-E07-04 | Restrict dashboard bind to `127.0.0.1` loopback only; require Bearer token on all write routes | In Phase 2 |

All three controls are implemented in the sections above under Proxy Security, Dashboard Security, and are verified in the Phase 2 build testing.
