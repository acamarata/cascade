package cmd

import "os"

// osLookupEnv is the production envLookup: a thin wrapper over
// os.LookupEnv so runChat's real call site never reads the process
// environment directly (chat_test.go injects a fake instead). os.Getenv/
// os.LookupEnv are not part of internal/build/outputgate.go's denied
// selector set (only os.Stdout/os.Stderr and bare fmt.Print* are), so this
// wrapper exists for testability, not to satisfy that gate.
func osLookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
