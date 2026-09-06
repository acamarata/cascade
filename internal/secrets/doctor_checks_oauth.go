// Purpose: the oauth-not-expired half of the secrets doctor checks,
//
//	split from doctor_checks.go under the repo's 300-line file cap. It
//	owns enumerating the OAuth token records the vault holds and
//	comparing each declared expiry against the injected clock.
//
// Inputs: the shared DoctorCheckDeps.
// Outputs: a doctor.CheckResult naming providers and accounts, never a
//
//	token. The stored record holds vault key NAMES only, which is what
//	makes reading it on a health path safe.
//
// Constraints: a record that cannot be decoded is an error, never a
//
//	skipped entry: a grant whose expiry cannot be read is a grant whose
//	validity is unknown (Art.1).
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (oauth-not-expired).

package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// oauthExpiryWindow is how far ahead the oauth check looks. A token
// expiring inside the window is reported now, while there is still time
// to refresh it.
const oauthExpiryWindow = 24 * time.Hour

// oauthRecordSuffix is the vault name suffix of an OAuth token record.
const oauthRecordSuffix = ".record"

// oauthNotExpiredCheck reports stored OAuth grants at or near expiry.
type oauthNotExpiredCheck struct{ deps DoctorCheckDeps }

func (oauthNotExpiredCheck) Name() string { return "secrets/oauth-not-expired" }

func (oauthNotExpiredCheck) Describe() string {
	return "reports every stored OAuth grant already past its expiry or expiring within twenty-four hours"
}

func (oauthNotExpiredCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

func (oauthNotExpiredCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run enumerates the token records the OAuth broker files in the vault
// and compares each record's declared expiry against the injected clock.
// A record it cannot decode is an error, never a skipped entry: a grant
// whose expiry cannot be read is a grant whose validity is unknown.
func (c oauthNotExpiredCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	records, err := oauthRecordNames(ctx, c.deps.Broker)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "could not list the vault's OAuth records", Detail: err.Error()}, nil
	}
	now := c.deps.Now()
	var expired, soon, undecodable []string
	for _, name := range records {
		rec, derr := readOAuthRecord(ctx, c.deps.Broker, name)
		switch {
		case derr != nil:
			undecodable = append(undecodable, name)
		case rec.ExpiresAt.IsZero():
		case !rec.ExpiresAt.After(now):
			expired = append(expired, rec.Provider+"/"+rec.Account)
		case rec.ExpiresAt.Before(now.Add(oauthExpiryWindow)):
			soon = append(soon, rec.Provider+"/"+rec.Account)
		}
	}
	return oauthResult(len(records), expired, soon, undecodable)
}

// oauthResult turns the three collected sets into one result.
func oauthResult(total int, expired, soon, undecodable []string) (doctor.CheckResult, error) {
	sort.Strings(expired)
	sort.Strings(soon)
	sort.Strings(undecodable)
	if len(expired) > 0 || len(undecodable) > 0 {
		detail := strings.Join(append(append([]string{}, expired...), undecodable...), ", ")
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     fmt.Sprintf("%d expired and %d unreadable OAuth grant(s) of %d", len(expired), len(undecodable), total),
			Detail:      detail,
			Remediation: "re-authorize the affected accounts",
		}, nil
	}
	if len(soon) > 0 {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     fmt.Sprintf("%d OAuth grant(s) expire within 24h", len(soon)),
			Detail:      strings.Join(soon, ", "),
			Remediation: "refresh or re-authorize the affected accounts before they lapse",
		}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: fmt.Sprintf("%d stored OAuth grant(s), none expiring within 24h", total)}, nil
}

// oauthRecordNames returns the vault names holding an OAuth token record.
func oauthRecordNames(ctx context.Context, broker *Broker) ([]string, error) {
	names, err := broker.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, oauthKeyPrefix) && strings.HasSuffix(name, oauthRecordSuffix) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// readOAuthRecord decodes one stored record. The record holds vault key
// NAMES, never a token, which is what makes reading it here safe.
func readOAuthRecord(ctx context.Context, broker *Broker, name string) (provider.TokenRecord, error) {
	raw, err := internalGet(ctx, broker, name)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	var rec struct {
		provider.TokenRecord
	}
	if jerr := json.Unmarshal(raw, &rec); jerr != nil {
		return provider.TokenRecord{}, cascade.Wrapf(cascade.KindIntegrity, jerr,
			"secrets: the stored OAuth record %q could not be decoded", name)
	}
	return rec.TokenRecord, nil
}
