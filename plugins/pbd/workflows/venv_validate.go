// Package workflows provides virtual-environment helpers.
package workflows

import (
	"errors"
	"io"
	"path/filepath"
)

// ErrVenvMissing signals an incomplete virtual environment directory.
var ErrVenvMissing = errors.New("D5010")

// ErrVenvInstall signals invalid arguments or a failed creation step.
var ErrVenvInstall = errors.New("D5011")

// EnsureVenv validates inputs, checks for an existing venv at dataHome/pytools,
// and returns the target path. It never calls real CLIs or mutates state when
// the venv is already complete.
func EnsureVenv(dataHome, python string, dryRun bool, env Env, out io.Writer) (string, error) {
	if env == nil || out == nil {
		return "", ErrVenvInstall
	}
	if dataHome == "" || filepath.Clean(dataHome) != dataHome || !filepath.IsAbs(dataHome) {
		return "", ErrVenvInstall
	}
	allowed := map[string]bool{
		"/opt/homebrew/bin/python3": true,
		"/usr/bin/python3":          true,
	}
	if !allowed[python] {
		return "", ErrVenvInstall
	}

	target := filepath.Join(dataHome, "pytools")

	existsTarget := env.Exists(target)
	existsCfg := env.Exists(filepath.Join(target, "pyvenv.cfg"))
	existsPy := env.Exists(filepath.Join(target, "bin", "python"))
	existsPip := env.Exists(filepath.Join(target, "bin", "pip"))

	allPresent := existsTarget && existsCfg && existsPy && existsPip
	anyPresent := existsTarget || existsCfg || existsPy || existsPip

	if allPresent {
		return target, nil
	}
	if anyPresent {
		return "", ErrVenvMissing
	}

	err := env.Run(python, "-m", "venv", target)
	if err != nil {
		return "", ErrVenvInstall
	}

	err = env.Run(filepath.Join(target, "bin", "pip"), "install", "pytest=="+PytestVersion)
	if err != nil {
		return "", ErrVenvInstall
	}

	return target, nil
}
