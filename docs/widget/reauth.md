# Signing a profile back in from the widget

A profile row that needs a new sign-in is amber and has a **Sign in** button.
The button opens a confirmation first. Nothing runs until you confirm.

## What you see and what happens

1. Tap **Sign in** on an amber row. The popover shows the row's label and the
   exact command, for example `cascade provider reauth acct-a-pool`.
2. **Cancel** closes the confirmation and launches nothing.
3. **Open Terminal and run** writes a one-time command file and asks Terminal to
   open it. Terminal then runs `cascade provider reauth <ref>` using the cascade
   binary that `cascade widget install` recorded.

The ref in the command is the row's opaque reference, not a provider name.
Email-named providers appear as `ref-` plus 12 hex characters. The cascade CLI
maps the ref back to the one provider it names; the widget never does.

## What the launcher checks

All of these must hold, in this order, or nothing is written and nothing opens:

| Check | Refusal |
|---|---|
| The ref is 1-64 characters of `A-Z a-z 0-9 . _ -` and does not start with `-` | the profile reference is not valid |
| The ref belongs to a row in the current snapshot whose `reauth_required` is true | that profile no longer needs sign-in |
| `CASCADE_CLI_PATH` is set, absolute, free of quotes and control characters, and an executable file | the installed cascade command is unavailable |
| `CASCADE_HOME`, when set, is absolute and free of quotes and control characters | the installed cascade command is unavailable |
| The bundled `cascade-reauth.sh` has a safe absolute path and is executable | the sign-in script is missing or has an unsafe path |
| The command file is created in `~/Library/Caches/cascade` | the command file could not be created |
| `/usr/bin/open -a Terminal <file>` exits 0 | Terminal could not be opened |

Each refusal is one line in red at the top of the popover. The text is fixed;
it never repeats the ref.

If the daemon snapshot changes while the confirmation is open and the row no
longer needs sign-in, the confirmation closes by itself.

## The command file

`~/Library/Caches/cascade/reauth-<UUID>.command`, directory and file mode
`0700`. The file is created with `O_EXCL | O_NOFOLLOW` relative to the verified
directory, so a second file of the same name, or a planted link, fails. It has
three lines, four when `CASCADE_HOME` is recorded:

```
#!/bin/sh
rm -f "$0"
export CASCADE_HOME='<absolute path>'     # only when recorded
exec '<bundle>/Contents/Resources/cascade-reauth.sh' '<cli path>' '<ref>'
```

It deletes itself first, so it runs once. If Terminal never runs it, the file
stays in the cache directory and holds only the ref and the two paths.

`cascade-reauth.sh` checks its arguments again (exactly two; absolute, executable
CLI path without control characters; same ref guard, ASCII only in any locale) and then runs `<cli> provider reauth <ref>` with no shell
parsing of the ref.

## Limits

- The confirmation shows the label with control and bidirectional-format
  characters removed. A Confirm button launches only the request its own view
  displays; a view that is a moment behind the other one does nothing and then
  catches up.
- Terminal runs the file in your login shell. When the installer recorded no
  `CASCADE_HOME`, a `CASCADE_HOME` exported by your own shell profile still
  reaches the CLI, as it does when you type `cascade` yourself.
- The launcher never searches `PATH` for cascade. If `CASCADE_CLI_PATH` is not
  recorded, run `cascade widget install` again.
- Providers stored with an API key refuse the flagless form with the exact
  `--key-env` command to run instead. That message appears in the Terminal
  window.
- The widget does not wait for the sign-in to finish. The row turns normal when
  the daemon reports the lane available.
