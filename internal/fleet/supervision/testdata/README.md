# internal/fleet/supervision — test data provenance

## Tier-2 pseudo-terminal (`tier2_pty_test.go`)

**Exercised live. No recorded fixture, and there cannot be one.**

Art.2 requires an external contract to be captured from a real counterpart
rather than described. The counterpart here is an OS pseudo-terminal, which
is a live kernel object, not a wire format: there are no bytes to record
that would prove a terminal can be opened, written to and read from on the
host running the test. So the test exercises the real device directly.

| | |
| --- | --- |
| Module | `github.com/creack/pty` |
| Version | `v1.1.24` |
| Licence | MIT (verified against the module's own LICENSE file; registered in `internal/build/licenses_registry.go`) |
| Why this module | R-16.54 named it: pure Go syscalls, no cgo, which is what keeps the core CGO-free (repo hard rule 2) |
| Platforms | `darwin`, `linux` only — 06-FORGE-SPEC §2's tier-2 scope. Windows asserts the refusal instead (`tier2_windows_test.go`) |

### Which side is which

`pty.Open()` returns the CONTROLLER side and the TERMINAL side of one
device. The supervisor holds the controller side: it writes the question
there and reads the answer there. The test plays the person, so it writes
to the TERMINAL side.

This is worth stating because getting it backwards produces a test that
passes for the wrong reason. A test that wrote the answer to the controller
side would be talking to itself, the supervisor would read nothing, and the
"denied" case would still pass — no answer read is also not a yes. The
approval case is what catches the mistake, which is why both directions are
tested over the real device rather than only the refusal.

### If the test skips

`realTerminal` skips when the host cannot open a pseudo-terminal at all
(a container with no `/dev/ptmx`, or an exhausted device limit). That is a
host limitation rather than a product failure, and the skip says which.
A skip is not a pass: the CI lane that matters for this test is one where
the device exists, and `go test -run TestTier2RealPTY` is listed in the
ticket's own checks for that reason.
