// Purpose: tests for the oauth-not-expired check, split from
//
//	doctor_checks_test.go under the repo's 300-line file cap.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (oauth-not-expired tests).

package secrets

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/provider"
)

// seedOAuthRecord writes one record under the OAuth broker's own naming
// scheme, so the check enumerates exactly what the real broker files.
func seedOAuthRecord(t *testing.T, deps DoctorCheckDeps, account string, exp time.Time) {
	t.Helper()
	raw, err := json.Marshal(provider.TokenRecord{
		Provider: "acme", Account: account, AccessRef: "oauth.acme." + account + ".access.1", ExpiresAt: exp,
	})
	if err != nil {
		t.Fatalf("encoding the record: %v", err)
	}
	name := oauthKeyPrefix + "acme." + account + oauthRecordSuffix
	if _, serr := deps.Broker.Set(context.Background(), name, raw, SetUpdate); serr != nil {
		t.Fatalf("seeding %s: %v", name, serr)
	}
}

func TestOAuthNotExpiredGradesEveryState(t *testing.T) {
	now := doctorTestNow()
	for _, tc := range []struct {
		name string
		exp  time.Time
		want doctor.Status
	}{
		{"comfortably valid", now.Add(72 * time.Hour), doctor.StatusOK},
		{"no declared expiry", time.Time{}, doctor.StatusOK},
		{"expiring soon", now.Add(2 * time.Hour), doctor.StatusWarn},
		{"already expired", now.Add(-time.Hour), doctor.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newDoctorDeps(t)
			seedOAuthRecord(t, deps, "primary", tc.exp)
			res, err := checkNamed(t, deps, "secrets/oauth-not-expired").Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Status != tc.want {
				t.Fatalf("Run = %+v, want %s", res, tc.want)
			}
		})
	}
}

func TestOAuthNotExpiredReportsAnUnreadableRecord(t *testing.T) {
	deps := newDoctorDeps(t)
	name := oauthKeyPrefix + "acme.broken" + oauthRecordSuffix
	if _, err := deps.Broker.Set(context.Background(), name, []byte("{not json"), SetUpdate); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	res, err := checkNamed(t, deps, "secrets/oauth-not-expired").Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run = %+v, want an error: a grant whose expiry cannot be read has unknown validity", res)
	}
	if _, ferr := checkNamed(t, deps, "secrets/oauth-not-expired").Fix(context.Background()); ferr != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix = %v", ferr)
	}
}

func TestOAuthNotExpiredIsOKOnAnEmptyVault(t *testing.T) {
	res, err := checkNamed(t, newDoctorDeps(t), "secrets/oauth-not-expired").Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("Run = (%+v, %v)", res, err)
	}
}
