# Completion-gate hook

The completion gate decides whether a harness may finish a task or stop. The
harness runs one command for the `TaskCompleted` and `Stop` hook events. That
command asks the daemon, and the harness blocks unless the daemon says yes.

## The command

The hook pack installs this for each event, with the absolute path of the
cascade binary and the daemon socket filled in at render time:

```sh
'/abs/path/to/cascade' fleet completion-check --event Stop --socket '/path/to/daemon.sock' || exit 2
```

- The binary is named by `filepath.Clean(os.Executable())`, an absolute path
  with no `PATH` lookup or additional symlink resolution. On darwin this keeps
  the installation symlink usable after a package-manager upgrade removes the
  old versioned target. On Linux, `os.Executable()` already resolves
  `/proc/self/exe`; that platform limitation is accepted. A removed binary
  still fails closed with exit 2.
- Both paths are single-quoted for POSIX sh, including paths that hold quotes,
  spaces or other shell characters. Rendering refuses an unquoted socket placeholder.
- `fleet completion-check` is hidden from `--help`. It exists for the hook, not
  for people.

## What it does

1. Reads the harness hook JSON from stdin (at most 1 MiB) and decodes it. The
   only field it uses is `stop_hook_active`. A stdin that is empty, too large or
   not a JSON object counts as `false`. The read stops after one second so an
   open stdin leaves time for the daemon call. The flag never changes the decision.
2. Builds the request with `encoding/json` from the event and three environment
   variables: `CASCADE_SESSION_ID`, `CASCADE_JOB_ID` and `CASCADE_TICKET_ID`.
   An ordinary human session leaves the last two unset. Whatever the variables
   hold travels as plain string values, so quotes, backslashes, newlines and
   shell characters cannot add or change a field or run anything.
3. Makes one `fleet.sessions.completion_check` call over the daemon socket.
4. Decodes the reply into a typed result. The reply is JSON, never matched as
   text, so field order and spacing do not matter.

## Exit codes

| Outcome | Exit | stderr |
|---|---|---|
| The reply says `deny: false` | 0 | nothing |
| The reply says `deny: true` | 2 | the daemon's reason, on one line |
| No reply: socket missing, daemon stopped, refused connection | 2 | `request failed (<kind>)` |
| No reply inside the deadline | 2 | `request failed (timeout)` |
| A reply that is not valid, has no `deny`, or has a `deny` that is not a boolean | 2 | `response malformed` or `request failed (...)` |
| A JSON-RPC error from the daemon | 2 | `request failed (<kind>)` |
| A reply larger than 4 MiB | 2 | `request failed (internal)` |
| Bad flags or an unknown event | 2 | the reason |
| The binary is missing or not executable, or the process crashes | 2 | from the shell or the crash |

Only an explicit `deny: false` exits 0. The trailing `|| exit 2` turns any exit
the subcommand did not choose, such as a crash, a signal or a missing binary,
into exit 2 as well. A reason from the daemon has C0, DEL, C1, and Unicode line
and paragraph separators replaced by spaces and is cut at 2048 bytes, so stderr
is always one line.

## Deadline

The call is bounded by the daemon's completion timeout (10 seconds) plus five
seconds, so the client never gives up before the daemon could have answered
with its own timeout denial.

## One request per invocation

The command is a single straight-line invocation. A `Stop`, block, `Stop`
replay by the harness makes a new invocation, and each one makes one request.
Nothing in the command retries.

## Checking it by hand

Run the installed command in a shell with the environment variables set, and
read `$?`:

```sh
CASCADE_JOB_ID=job-1 sh -c '<the installed command>' < /dev/null; echo $?
```

With the daemon stopped, the answer is 2.
