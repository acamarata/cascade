# `cascade uninstall`

`cascade uninstall [--dry-run] [--yes] [--purge-data] [--json]` removes what
Cascade installed on this machine, item by item, and nothing else. Backups are
never touched.

## What it removes

By default, in this order:

1. The daemon service unit (`~/Library/LaunchAgents/com.acamarata.cascade.plist`
   on macOS, `~/.config/systemd/user/cascade.service` on Linux), through the same
   installer `cascade daemon uninstall` uses. A unit Cascade did not write is
   refused.
2. The menu-bar widget LaunchAgent (macOS only).
3. Each installed plugin, through `plugin remove`'s path: the plugin's teardown
   hook runs first, then its record is deleted.
4. Each vault entry the custody index lists. Entries filed under another
   service are never named, so they are never deleted.
5. The executables the install receipt lists (`cascade-elevate-helper`,
   `cascade-github`, then `cascade`), for a `script` install only and only
   when the file's sha256 equals its receipt entry.
6. The install receipt, once no file it lists is left behind.

`--purge-data` adds the data-class items: the elevation key file, the config
file and the data directory. Backups inside the data directory (any `backups`
directory, and every configured filesystem backup target) are kept, and the
directory itself stays when it still holds one.

## What it keeps

| Case | Outcome |
| --- | --- |
| An executable whose sha256 differs from the receipt | kept, `modified since install; rerun install.sh, then cascade uninstall` |
| A Homebrew install | kept, `brew uninstall cascade` |
| No install receipt | the running binary is kept; delete it by hand |
| A receipt whose `install_dir` is relative, a symlink, or not the running binary's directory | every executable and the receipt kept; the run fails with an integrity error |
| A receipt naming a file outside the fixed executable set, a path, or `..` | the same refusal |
| A symlink at an executable's path | kept; the link is never followed |
| Any path inside a backup location | never listed, and refused if reached |

A kept item is printed with its note, so every file left behind is named.

## Confirmation and elevation

Without `--yes` the full plan is printed and one `y/N` question is asked.
`--yes` accepts the default: install items go, data stays.
`CASCADE_NO_INPUT=1` without `--yes` is an error before anything is listed
(`--dry-run` needs neither).

`--purge-data` is the elevated verb `uninstall.purge_data`. After the
confirmation and before any item runs, it needs local presence through the
enrolled elevation helper. A refusal removes nothing.

The platform elevation key held by the elevation helper is not removed by
this build: the data item covers the file-tier key only, and the note says so.

## Dry run

`--dry-run` lists every item with `would-remove`, `absent` or `kept` and changes
nothing. With `--json` the result is `{"items":[{"kind","locator","action","note"}]}`.

Running `cascade uninstall` again after a successful run reports every item
`absent` and exits 0.

## Windows

There are no service units to remove. Vault entries in the encrypted file vault
are removed. `--purge-data` is refused; delete the cascade home directory by hand after
the uninstall. The running `cascade.exe` is kept with a note to delete it after
the command exits.

## Limits

A plugin's teardown hook is the plugin's own code. Cascade bounds how long it
may run, not which files it removes. A plugin is kept (and the run fails) when
its teardown cannot resolve the harness paths. The claude plugin's teardown
also resolves project-level artifacts from the current working directory, so
run the uninstall from the project root to remove those.
