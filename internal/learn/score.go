package learn

// Purpose: the decayed Beta capability scorer (R-16.37 §Fleet capacity,
//   R-16.18). One posterior per (scope_key, task_class, tier), seeded from
//   the ONE priors table in internal/fleet/capacity (never a second map),
//   decayed with a 30-day half-life, and resolved repo -> lang -> global.
// Inputs: a ScopeKey, a conductor.TaskClass, a capacity.Tier and an injected
//   runtime.Clock; the stored rows of jobs_capability_score_observations.
// Outputs: Posterior (alpha, beta, observation count, the level used) and
//   the posterior mean as a capacity.CapabilityScorer.
// Constraints: internal/learn imports internal/fleet/capacity, never the
//   reverse; a cell with no prior (segment, chat) reports ok=false and
//   scores 0.0, the capacity contract's most-restrictive value, instead of
//   inventing a default; a repo posterior is used only from
//   minRepoObservations observations; a read error scores 0.0 (fail closed).
// SPORT: internal.learn.SQLiteCapabilityScorer/ADDED, internal.learn.Posterior/ADDED,
//   internal.learn.PriorAlphaBeta/ADDED, internal.learn.ScopeKey/ADDED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// priorMass is the Beta(2,2)-strength seed: alpha0 + beta0 = 4.
const priorMass = 4.0

// minRepoObservations is the observation count a repo scope needs before its
// own posterior is used instead of the lang aggregate.
const minRepoObservations = 5

// ScopeKey names the level a posterior lives at: "repo:<opaque id>",
// "lang:<Language>" or "global". Build one with ParseScopeKey.
type ScopeKey string

// FallbackLevel is the level a posterior was read from.
type FallbackLevel string

// The three closed FallbackLevel members, in resolution order.
const (
	LevelRepo   FallbackLevel = "repo"
	LevelLang   FallbackLevel = "lang"
	LevelGlobal FallbackLevel = "global"
)

// Valid reports whether l is one of the three members.
func (l FallbackLevel) Valid() bool {
	return l == LevelRepo || l == LevelLang || l == LevelGlobal
}

const scopeGlobal ScopeKey = "global"

const (
	scopeRepoPrefix = "repo:"
	scopeLangPrefix = "lang:"
)

func repoScope(repoID string) ScopeKey  { return ScopeKey(scopeRepoPrefix + repoID) }
func langScope(lang Language) ScopeKey  { return ScopeKey(scopeLangPrefix + string(lang)) }
func (k ScopeKey) level() FallbackLevel { return levelOf(string(k)) }

func levelOf(s string) FallbackLevel {
	switch {
	case strings.HasPrefix(s, scopeRepoPrefix):
		return LevelRepo
	case strings.HasPrefix(s, scopeLangPrefix):
		return LevelLang
	}
	return LevelGlobal
}

// ParseScopeKey accepts exactly the three key forms and refuses anything
// else (KindInvalidInput): a repo id must be an opaque id, a language a
// member of the closed Language enum. The error never echoes the input.
func ParseScopeKey(s string) (ScopeKey, error) {
	switch {
	case s == string(scopeGlobal):
		return scopeGlobal, nil
	case strings.HasPrefix(s, scopeRepoPrefix):
		id := strings.TrimPrefix(s, scopeRepoPrefix)
		if id != "" && opaqueIDPattern.MatchString(id) {
			return ScopeKey(s), nil
		}
	case strings.HasPrefix(s, scopeLangPrefix):
		if _, err := DecodeLanguage(strings.TrimPrefix(s, scopeLangPrefix)); err == nil {
			return ScopeKey(s), nil
		}
	}
	return "", cascade.New(cascade.KindInvalidInput, "learn: scope key must be repo:<opaque id>, lang:<language> or global")
}

// PriorAlphaBeta returns the Beta seed for (tier, tc) from capacity.Priors:
// mass 4, alpha0 = 4p, beta0 = 4(1-p). ok is false for a cell the table does
// not hold (segment, chat, an unknown tier): no default is invented.
func PriorAlphaBeta(tier capacity.Tier, tc conductor.TaskClass) (alpha, beta float64, ok bool) {
	p, ok := capacity.Priors[tier][tc]
	if !ok {
		return 0, 0, false
	}
	return priorMass * p, priorMass * (1 - p), true
}

// Posterior is one resolved Beta posterior. Alpha and Beta already include
// the prior and the decayed observations; Observations is the raw count at
// Level.
type Posterior struct {
	Alpha, Beta  float64
	Observations int
	Level        FallbackLevel
}

// rowQuerier is the read surface shared by *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// scoreRow is one stored jobs_capability_score_observations row.
type scoreRow struct {
	alpha, beta float64
	count       int
	lastUpdated time.Time
}

// SQLiteCapabilityScorer reads (Posterior, Score) and writes (Observe)
// jobs_capability_score_observations through db, decaying with clock.
type SQLiteCapabilityScorer struct {
	db    *sql.DB
	clock runtime.Clock
}

// NewSQLiteCapabilityScorer returns a scorer over db using clock for decay.
func NewSQLiteCapabilityScorer(db *sql.DB, clock runtime.Clock) *SQLiteCapabilityScorer {
	return &SQLiteCapabilityScorer{db: db, clock: clock}
}

// Posterior resolves scope for (tc, tier): a repo scope is used from
// minRepoObservations observations, else the repo's language aggregate
// (from its latest outcome) when it holds data, else global. A lang scope
// falls back to global; global is always answerable (the prior when empty).
// A cell with no prior is KindNotFound.
func (s *SQLiteCapabilityScorer) Posterior(ctx context.Context, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier) (Posterior, error) {
	if s == nil || s.db == nil || s.clock == nil {
		return Posterior{}, cascade.New(cascade.KindInvalidInput, "learn: Posterior requires a constructed SQLiteCapabilityScorer")
	}
	if _, err := ParseScopeKey(string(scope)); err != nil {
		return Posterior{}, err
	}
	a0, b0, ok := PriorAlphaBeta(tier, tc)
	if !ok {
		return Posterior{}, cascade.New(cascade.KindNotFound, "learn: no capability prior for this (tier, task class)")
	}
	chain, err := s.chain(ctx, scope)
	if err != nil {
		return Posterior{}, err
	}
	now := s.clock.Now()
	for _, key := range chain {
		row, found, err := readRow(ctx, s.db, key, tc, tier)
		if err != nil {
			return Posterior{}, err
		}
		if !usable(key.level(), row, found) {
			continue
		}
		return Posterior{
			Alpha: a0 + decayed(row.alpha, row.lastUpdated, now), Beta: b0 + decayed(row.beta, row.lastUpdated, now),
			Observations: row.count, Level: key.level(),
		}, nil
	}
	return Posterior{Alpha: a0, Beta: b0, Level: LevelGlobal}, nil
}

// usable is the per-level acceptance rule: repo needs minRepoObservations,
// lang needs any observation, global is the chain's end (handled by the
// caller as the prior when no row exists).
func usable(level FallbackLevel, row scoreRow, found bool) bool {
	switch level {
	case LevelRepo:
		return found && row.count >= minRepoObservations
	case LevelLang:
		return found && row.count >= 1
	case LevelGlobal:
		return found
	}
	return false
}

// chain lists the scope keys to try, most specific first.
func (s *SQLiteCapabilityScorer) chain(ctx context.Context, scope ScopeKey) ([]ScopeKey, error) {
	switch scope.level() {
	case LevelRepo:
		out := []ScopeKey{scope}
		lang, found, err := s.repoLanguage(ctx, strings.TrimPrefix(string(scope), scopeRepoPrefix))
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, langScope(lang))
		}
		return append(out, scopeGlobal), nil
	case LevelLang:
		return []ScopeKey{scope, scopeGlobal}, nil
	case LevelGlobal:
		return []ScopeKey{scopeGlobal}, nil
	}
	return nil, cascade.New(cascade.KindInvalidInput, "learn: unknown scope level")
}

// repoLanguageQuery reads one repo's latest outcome language; the
// (repo_id, created_at) index serves it.
const repoLanguageQuery = `SELECT language FROM ` + tableTelemetryOutcomes +
	` WHERE repo_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`

// repoLanguage is the language of repoID's most recent recorded outcome.
func (s *SQLiteCapabilityScorer) repoLanguage(ctx context.Context, repoID string) (Language, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, repoLanguageQuery, repoID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "learn: read repo language for capability fallback")
	}
	lang, derr := DecodeLanguage(raw)
	return lang, derr == nil, nil
}

// readRow loads one observation row; found is false when none exists.
func readRow(ctx context.Context, q rowQuerier, key ScopeKey, tc conductor.TaskClass, tier capacity.Tier) (scoreRow, bool, error) {
	var r scoreRow
	var last int64
	err := q.QueryRowContext(ctx, `SELECT alpha, beta, observation_count, last_updated FROM `+tableCapabilityScore+
		` WHERE scope_key = ? AND task_class = ? AND tier = ?`, string(key), string(tc), string(tier)).
		Scan(&r.alpha, &r.beta, &r.count, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return scoreRow{}, false, nil
	}
	if err != nil {
		return scoreRow{}, false, cascade.Wrap(cascade.KindUnavailable, err, "learn: read jobs_capability_score_observations row")
	}
	r.lastUpdated = time.Unix(last, 0).UTC()
	return r, true, nil
}

// Score is the posterior mean alpha/(alpha+beta). Any read error, an
// invalid scope, or a cell with no prior scores 0.0 (most restrictive).
func (s *SQLiteCapabilityScorer) Score(scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier) float64 {
	p, err := s.Posterior(context.Background(), scope, tc, tier)
	if err != nil || p.Alpha+p.Beta <= 0 {
		return 0.0
	}
	return p.Alpha / (p.Alpha + p.Beta)
}

// For adapts the scorer to capacity.CapabilityScorer for one scope key. An
// unparseable key yields a scorer that scores 0.0 everywhere.
func (s *SQLiteCapabilityScorer) For(scopeKey string) capacity.CapabilityScorer {
	return scopeScorer{s: s, scope: ScopeKey(scopeKey)}
}

// scopeScorer closes over one scope key.
type scopeScorer struct {
	s     *SQLiteCapabilityScorer
	scope ScopeKey
}

// Score satisfies capacity.CapabilityScorer.
func (c scopeScorer) Score(tier capacity.Tier, tc conductor.TaskClass) float64 {
	return c.s.Score(c.scope, tc, tier)
}

var _ capacity.CapabilityScorer = scopeScorer{}
