# Agent callback channel

While the claude driver runs an agent in a leased tree, the harness can report what it is doing
(a tool started, a tool finished, the turn stopped) back to the driver. This page says how those
reports travel, what they are good for, and what they are not.

## The path

1. The driver opens a private unix socket in a fresh directory only it can enter, and writes a
   callback file, `.claude/cascade-callback.json`, into the leased tree. The file holds the socket
   path and a one-time session token. It is mode 0600 and owned by the running user.
2. The agent hook pack carries three entries (PreToolUse, PostToolUse, Stop). Each runs the hidden
   command `cascade hook relay <event>`. The pack is merged into the tree's `.claude/settings.json`;
   entries that were already there stay, and rendering twice changes nothing.
3. The relay reads the harness's hook JSON from stdin, looks for `.claude/cascade-callback.json`
   only in the project directory the harness names in `CLAUDE_PROJECT_DIR` (the leased tree root),
   and sends one frame to the socket. It never walks up from the working directory and never reads
   the payload's `cwd`, so a hook that runs inside another tree, or reaches it through a symlink,
   cannot use that tree's channel. The frame carries the
   event kind, the tool name, duration, the paths touched and a hash of the result. Its exit code
   field is always 0: the harness fires no PostToolUse for a failed call, so a frame never reports
   one.
4. Every frame starts with the token as a bearer prefix (it is not signed) and is numbered. The
   numbers come from a counter file beside the socket, under a lock, so they strictly increase even
   when hooks fire at once. The driver refuses a frame with a wrong token, a repeated number or a
   skipped-back number.

## What the relay refuses

- A callback file that is not a regular 0600 file owned by the caller with exactly one link (a
  symlink, a hard link to another tree's file, a looser mode or another user's file). Nothing is
  sent and the command exits non-zero.
- A `.claude` directory that is a symlink (for example to another tree's `.claude`) or is not owned
  by the caller.
- A socket that is not the caller's own, or that sits in a directory which is a symlink, is not
  owned by the caller, or grants any group or other permission.
- A counter file that is not a regular 0600 file owned by the caller with exactly one link, so a
  hard link to another file is never overwritten.
- A tree with no callback file, or no `CLAUDE_PROJECT_DIR` at all: the hook does nothing and exits
  0, so a hook outside a spawn is harmless.

## Known limitations

The relay trusts the project directory given by the harness environment. The socket path comes
from the callback file, which lives in the agent's tree, so the relay will dial any private socket
of the same user and create its counter file beside it; that gains no privilege, because it is the
same user and the frame has a fixed, constrained shape. On Windows the relay checks the directory
and file and then opens them, and that check-then-open race remains a known limitation.

If the driver does not answer before the relay's deadline, the frame may already be written, and
the next hook can send the following number on a new connection that the driver reads first. The
driver then sees the numbers out of order and fails the run, so a slow driver can end a run early.

The command never exits 2, because the harness reads 2 from a PreToolUse hook as "block this tool
call". It never prints the token, never takes it from the environment or the command line, and
never talks to the daemon's RPC socket.

## What a frame is worth

Callback telemetry is never evidence. The token file sits in the agent's own tree, so the agent
can read it and forge frames. Use frames for live status and for hints. Do not use one as proof that
work happened: no evidence check, CI gate or supervision rule may count a callback frame as such
proof. Proof comes from the leased tree's diff and the gates that run over it.
