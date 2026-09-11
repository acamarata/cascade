# internal/fleet/resume/testdata

Fixture provenance (Art.2 real-counterpart requirement).

## Kill -9 fan-out integration test (TestResumeKill9FanOut)

No static fixture files: the test builds its own real counterpart at run
time rather than replaying a captured artifact, because the thing under
test IS the real counterpart — a genuinely separate OS process, started
with `os/exec`, that opens the real on-disk SQLite journal store
(`providers/sqlite`, the same driver production uses), appends real
journal entries through the real `internal/fleet/journal` package, and is
then terminated with `(*os.Process).Kill()` (SIGKILL on the unix
platforms this test runs on — darwin/linux, matching Art.5's supported
tier for the daemon this package resumes).

- Tool: this repository's own `go test` binary, re-invoked as a
  subprocess via `exec.Command(os.Args[0], "-test.run=^TestResumeKillHelperProcess$")`,
  the same pattern `providers/sqlite/lock_crossprocess_test.go` established
  for its cross-process lock-arbitration proof (§D-3) and
  `internal/runtime/recovery_killnine_test.go` established for the
  Scan/RecoveryRegistry §D-24 proof. No new harness technique is
  introduced; this is the third use of an already-reviewed pattern.
- Version/date: whatever `go test` compiles at CI/build time; there is no
  separate pinned version, since the fixture IS the test binary.
- What it proves: a REAL SIGKILL (not a graceful shutdown, not
  `context.Cancel`) of a process that had already appended
  `fanout_leg_started`/`fanout_leg_done` journal entries for some but not
  all legs, followed by opening a fresh `journal.SQLiteStore` and
  `resume.Manager` against the SAME on-disk file the killed process used.
  This also exercises the real `providers/sqlite` exclusive-lock release
  path: the killed process held that lock and never ran its own deferred
  `Close`, so the resumer's `sqlite.Open` on the same path is the direct
  proof a stale advisory lock from a SIGKILLed holder does not deadlock
  the resumer (see this ticket's journal for the full account).

## Upgrade-in-place test (TestResumeUpgradeInPlace)

Also fixture-free for the same reason: it drives the SAME real
`journal.SQLiteStore`/`resume.Manager` pair through a graceful stop (no
kill) followed immediately by a second `Manager.Run` against the
identical store, standing in for D/S-07.T5's drain+exec-relaunch restart.
See this ticket's journal for the disclosed scope limit: this test does
NOT drive `cmd/cascade`'s real unix-socket JSON-RPC daemon process end to
end (that would require composing `platformDaemonRun`'s full daemon_unix.go
wiring, outside this ticket's files_scope, which lists only
`cmd/cascade/daemon.go`) — it proves the ResumeManager half of the
upgrade-in-place contract, not the drain/relaunch half D/S-07.T5 itself
owns.
