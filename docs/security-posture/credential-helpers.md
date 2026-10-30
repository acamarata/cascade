# Credential helpers

A helper is the owner's executable program. Configure an absolute path and its
SHA-256 digest in the account. The runner resolves symlinks, checks the target is
a regular executable owned by the running uid or root, and refuses group or
world write permission on the file or its parent directory. The configured
path's immediate parent is checked too. The resolved target is hashed and is
the executable path passed to the process. Changed bytes refuse execution.

The helper receives no arguments, no shell wrapper and no inherited environment.
Standard input is the null device. The environment consists of exactly:

- `PATH=/usr/bin:/bin:/usr/sbin:/sbin`
- `HOME` supplied by the caller
- `USER` and `LOGNAME` supplied by the caller
- `LANG=C.UTF-8`

The default timeout is ten seconds. Cancellation, timeout and output overflow
kill the helper's process group; process waiting has a one-second delay bound.
Stdout is read through a 4,097-byte limit. Output exceeding 4,096 bytes is
refused immediately. One trailing newline is trimmed; the remaining line must
be nonempty and contain no newline, carriage return or NUL. Stderr is drained
and discarded. Errors name the path and execution status, never output bytes.
No result is cached or logged; each token request invokes the helper again.
The caller must zero the returned bytes after use.

Windows refuses helper execution with `unsupported`; use `key_env`.

Residual risks: filesystem checks, hashing and execution are separate operations,
so a same-uid process can race them (TOCTOU). Process groups cannot contain a
program that deliberately escapes into a new session. The helper remains
runnable by any same-uid process, including an agent. A gateway-class agent's
own tool subprocesses can see its token. The follow-up isolation path is
`DEF-P2-claude-apikeyhelper-channel`.
