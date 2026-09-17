// Package acceptance holds the cross-subsystem acceptance drills: the runs
// that exercise the SHIPPED BINARY end to end against a real counterpart,
// rather than one package's own seams.
//
// Every drill in here has the same three-part shape, first established by
// the laptop⇄server sync drill (P1-E17-W4-S38-T5):
//
//  1. A target RESOLVER that fails closed. A drill whose real counterpart
//     is unconfigured returns a typed error naming exactly what to set. It
//     never substitutes a local stand-in and calls that the acceptance —
//     the whole value of an acceptance ticket is that it ran against the
//     real thing (Art.2).
//  2. One SCRIPT, run by every lane. The rehearsal and the real drill
//     differ in their counterpart and in nothing else, so a rehearsal that
//     passes is evidence the script works.
//  3. A REHEARSAL lane that runs without the owner prerequisite, so the
//     script is exercised on every commit rather than only on the day
//     somebody has a credential.
//
// Nothing in this package is compiled into the shipped binary: it is
// test-only, and its files carry the `integration` build tag because they
// run real subprocesses and speak real HTTP.
package acceptance
