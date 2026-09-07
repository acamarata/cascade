package transport_test

// Purpose: TestMain for the transport test binary, which points HOME at a
//   throwaway directory for every test in the package.
// Constraints: this is not a convenience. Both transports build the real
//   response marshaler, which opens THIS PROCESS's vault at the data
//   directory resolved from $HOME. On a host with an OS keychain -- the
//   darwin dev machine -- custody takes the keychain branch and creates
//   nothing, so these tests looked clean locally. On a linux host there is
//   no keychain, custody falls back to the encrypted file vault, and that
//   CREATES $HOME/.cascade/data. That is an Art.7.1 violation which failed
//   CI's redirected-HOME job on every run while being invisible to every
//   command run on the dev machine; it was found by running the suite in a
//   linux container with a FILE planted at $HOME/.cascade, so the mkdir
//   failed and named the tests doing it.
//   TestMain rather than a per-test t.Setenv because the leak belongs to
//   the transports themselves, so every test in this package that serves a
//   request has it, including ones not yet written.
// SPORT: internal/mcp/transport test-support (ADD).

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "cascade-transport-home")
	if err != nil {
		panic("transport tests: could not create a throwaway HOME: " + err.Error())
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic("transport tests: could not redirect HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
