package runtime

import "fmt"

// Purpose: the Profile enum (local|server|worker) and its resolution
//   cascade (flag > CASCADE_PROFILE env > config.toml [runtime].profile >
//   default "local").
// Inputs: ResolveProfile takes the --profile flag value, an env accessor,
//   and the config-file value, in strict precedence order.
// Outputs: a validated Profile plus the ConfigSource it was resolved from,
//   or a typed InvalidProfileError naming the offending value and source.
// Constraints: an unrecognised non-empty value at ANY level is a hard,
//   typed error (T-1 contract task 2) — never silently coerced to the
//   default. An empty value at a given level means "not set", and falls
//   through to the next level.
// SPORT: runtime/profiles (ADD, placeholder per T-1 sport_updates).

// Profile is the runtime execution profile. 02-TARGET-STRUCTURE §Profiles:
// local and server run the same binary with different storage/provider
// wiring; worker enrolls against a controller and never resolves local
// storage.
type Profile string

const (
	// ProfileLocal is the zero-config, single-user profile: SQLite, local
	// filesystem, in-memory queue.
	ProfileLocal Profile = "local"
	// ProfileServer is the multi-user profile: Postgres/pgvector, S3,
	// Redis.
	ProfileServer Profile = "server"
	// ProfileWorker enrolls against a controller and holds no local
	// storage of its own.
	ProfileWorker Profile = "worker"

	// DefaultProfile is used when no flag, env var, or config file value
	// resolves a profile (08-INIT-CONFIG-SPEC §2).
	DefaultProfile = ProfileLocal
)

// Valid reports whether p is one of the three recognised profile values.
func (p Profile) Valid() bool {
	switch p {
	case ProfileLocal, ProfileServer, ProfileWorker:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (p Profile) String() string { return string(p) }

// InvalidProfileError reports an unrecognised profile value together with
// the resolution level it came from, so the message points the user at the
// exact place to fix it (flag, env, or config file).
type InvalidProfileError struct {
	Value  string
	Source ConfigSource
}

// Error implements the error interface.
func (e *InvalidProfileError) Error() string {
	return fmt.Sprintf("invalid profile %q from %s (must be one of: local, server, worker)", e.Value, e.Source)
}

// profileEnv is the subset of the process environment ResolveProfile
// reads: a single CASCADE_PROFILE lookup. Factoring it out lets callers
// inject a fake environment in tests (Art.7.1) instead of mutating the
// real process environment.
type profileEnv func(key string) (value string, ok bool)

// ResolveProfile implements the profile-resolution cascade: --profile flag
// > CASCADE_PROFILE env > config.toml [runtime].profile > default "local".
// An empty value at a given level is treated as "not set" and falls
// through to the next level; an unrecognised non-empty value at any level
// is a hard, typed error.
func ResolveProfile(flag string, env profileEnv, fileValue string) (Profile, ConfigSource, error) {
	if flag != "" {
		p := Profile(flag)
		if !p.Valid() {
			return "", SourceFlag, &InvalidProfileError{Value: flag, Source: SourceFlag}
		}
		return p, SourceFlag, nil
	}
	if env != nil {
		if v, ok := env("CASCADE_PROFILE"); ok && v != "" {
			p := Profile(v)
			if !p.Valid() {
				return "", SourceEnv, &InvalidProfileError{Value: v, Source: SourceEnv}
			}
			return p, SourceEnv, nil
		}
	}
	if fileValue != "" {
		p := Profile(fileValue)
		if !p.Valid() {
			return "", SourceFile, &InvalidProfileError{Value: fileValue, Source: SourceFile}
		}
		return p, SourceFile, nil
	}
	return DefaultProfile, SourceDefault, nil
}
