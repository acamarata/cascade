package log

// Spec represents an execution specification for running a command.
type Spec struct {
	Args []string
	Env  []string
}

// Runner is a function that takes a Spec and returns an exit code and error.
type Runner func(Spec) (int, error)

// CodexSpec builds a Spec for running codex with a private CODEX_HOME directory.
// It ignores any CODEX_HOME in the caller environment and always uses the provided privateDir.
func CodexSpec(privateDir string, callerEnv []string) Spec {
	env := make([]string, 0, len(callerEnv)+1)
	for _, e := range callerEnv {
		if len(e) >= 11 && e[:11] == "CODEX_HOME=" {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "CODEX_HOME="+privateDir)

	return Spec{
		Args: []string{"codex"},
		Env:  env,
	}
}

// CodexRunsViaInjectedRunner runs codex using the injected runner with a private CODEX_HOME.
func CodexRunsViaInjectedRunner(privateDir string, callerEnv []string, runner Runner) (int, error) {
	spec := CodexSpec(privateDir, callerEnv)
	return runner(spec)
}
