// Purpose: the six security doctor checks mounted in productionCheckRegistry:
// elevation custody tier, audit chain, policy profile, approval key,
// audit-only egress classes and the caller principal threat model.
// Inputs: the path provider and clock doctor holds. Outputs: doctor.Check values.
// Constraints: READ-ONLY BY CONSTRUCTION: no writing custody, firewall, broker,
//
//	runtime store, migrator or store flock. The only store open is
//	runtime.OpenReadOnlyStore, the vault read SelectCustodyReadOnly, the tier read elevation.SelectCustody.
//	A subject a check cannot read is a warning or an error, never an OK.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (P1-SEC-12).

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Seams a test replaces so no host keychain, helper or presence key is read.
var (
	doctorSelectCustody        = productionDoctorSelect
	doctorBoundFingerprint     = boundTrustFingerprint
	doctorApprovalKeyCustody   = secrets.SelectCustodyReadOnly
	presenceResidualSentence   = "A process running as your user can replace presence.key and trust.json together with a key whose passphrase it knows; cascade detects this only as a changed enrolment fingerprint in doctor and the audit log."
	presenceEnrolAction        = "elevation.presence.enroll"
	presenceEnrolActor         = "elevate-helper"
	errDoctorNoDataDir         = "the cascade data directory could not be resolved"
	callerPrincipalThreatModel = "caller principals protect model-facing surfaces against cross-session leakage and prompt-driven misuse; they are not a boundary against shell code running as the same user (ADR-0006)"
)

// securityDoctorCheck is the one shape all six checks share.
type securityDoctorCheck struct {
	name, describe string
	run            func(ctx context.Context) doctor.CheckResult
}

func (c securityDoctorCheck) Name() string     { return c.name }
func (c securityDoctorCheck) Describe() string { return c.describe }
func (c securityDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	return c.run(ctx), nil
}
func (securityDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
func (securityDoctorCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

func result(status doctor.Status, remedy, format string, args ...any) doctor.CheckResult {
	return doctor.CheckResult{Status: status, Message: fmt.Sprintf(format, args...), Remediation: remedy}
}

// newElevationCustodyDoctorCheck reports the custody tier elevated verbs
// would use (ADR-0005) without selecting it for writing.
func newElevationCustodyDoctorCheck(paths runtime.PathProvider, clock runtime.Clock) doctor.Check {
	return securityDoctorCheck{"elevation_custody", "elevation custody tier (ADR-0005)", func(ctx context.Context) doctor.CheckResult {
		c := doctorSelectCustody(paths.DataDir())
		switch c.Tier() {
		case elevation.CustodyPlatform:
			if _, err := doctorBoundFingerprint(c, paths.DataDir(), clock); err != nil {
				return result(doctor.StatusWarn, "run cascade elevate-helper --enroll", "tier=platform (source %s) but no enrolled key is bound to it: %v; elevated verbs refuse", c.Source(), err)
			}
			return result(doctor.StatusOK, "", "tier=platform (source %s): hardware-backed custody, enrolled and bound", c.Source())
		case elevation.CustodyPresence:
			return presenceCustodyResult(ctx, paths, clock, c)
		case elevation.CustodyFile:
			return result(doctor.StatusWarn, "run cascade elevate-helper --enroll --replace-file-tier",
				"tier=file retired tier: elevated verbs refuse; run cascade elevate-helper --enroll --replace-file-tier")
		case elevation.CustodyNone:
		}
		return result(doctor.StatusWarn, "run cascade elevate-helper --enroll", "tier=none: %s; elevated verbs refuse", c.Reason())
	}}
}

// presenceCustodyResult is the presence-tier row: the ADR-0005 residual plus the audited enrolment history.
func presenceCustodyResult(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock, c elevation.Custody) doctor.CheckResult {
	base := fmt.Sprintf("tier=presence (source %s): weaker than hardware custody. %s", c.Source(), presenceResidualSentence)
	fp, err := doctorBoundFingerprint(c, paths.DataDir(), clock)
	if err != nil {
		return result(doctor.StatusWarn, "run cascade elevate-helper --enroll --presence", "%s The enrolment is not bound to the presence key: %v", base, err)
	}
	enrols, err := auditedPresenceEnrolments(ctx, paths)
	switch {
	case err != nil:
		return result(doctor.StatusWarn, "run cascade doctor again after the audit chain is healthy", "%s The audited enrolments could not be read: %v", base, err)
	case len(enrols) == 0:
		return result(doctor.StatusWarn, "re-enrol with cascade elevate-helper --enroll --presence", "%s The enrolled fingerprint %s has no audited enrolment.", base, fp)
	}
	if last := enrols[len(enrols)-1]; last.fingerprint != fp {
		return result(doctor.StatusWarn, "if you did not re-enrol, treat the elevation key as replaced", "%s The enrolled fingerprint %s differs from the last audited enrolment %s.", base, fp, last.fingerprint)
	}
	for i := 1; i < len(enrols); i++ {
		if enrols[i].kind != "rotation" {
			return result(doctor.StatusWarn, "if you did not delete and re-enrol the key, treat it as replaced",
				"%s A fresh enrolment %s was audited after the earlier enrolment %s (the key was deleted and enrolled again).", base, enrols[i].fingerprint, enrols[i-1].fingerprint)
		}
	}
	return result(doctor.StatusWarn, "", "%s fingerprint=%s", base, fp)
}

// productionDoctorSelect is the production selector: elevation.SelectCustody
// only ranks sources and probes availability; it never enrols or writes.
func productionDoctorSelect(dataDir string) elevation.Custody {
	return elevation.SelectCustody(dataDir)
}

// boundTrustFingerprint is the enrolled key's fingerprint when the trust
// record is bound to custody c; BoundTrust is the only reader that decides it.
func boundTrustFingerprint(c elevation.Custody, dataDir string, clock runtime.Clock) (string, error) {
	fp, _, err := elevation.BoundTrust(c, elevation.NewFileBackend(dataDir), clock)
	return fp, err
}

// newPolicyDoctorCheck loads config.toml's [policy] section the way the
// daemon does and reads the stored deny rules, without building an engine.
func newPolicyDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return securityDoctorCheck{"policy", "policy profile and deny-store health", func(ctx context.Context) doctor.CheckResult {
		fix := "fix config.toml's [policy] section (see docs/config-reference.md) and run cascade doctor again"
		cfg, err := loadPolicyConfig(ctx, paths)
		if err != nil {
			return result(doctor.StatusWarn, fix, "policy: config.toml could not be loaded: %v", err)
		}
		controller := policy.NewController(nil)
		if err := applyAutonomyProfile(ctx, controller, cfg); err != nil {
			return result(doctor.StatusWarn, fix, "policy: the [policy] section is invalid: %v", err)
		}
		profile := controller.Profile().Name()
		store, closeStore, found, err := openDoctorStore(ctx, paths)
		if err != nil || !found {
			if err != nil {
				return result(doctor.StatusWarn, "restore the policy store or restart the daemon", "policy profile %s; the deny store could not be read: %v", profile, err)
			}
			return result(doctor.StatusOK, "", "policy profile %s; no deny rules stored yet", profile)
		}
		defer func() { _ = closeStore() }()
		deny, err := policy.NewStoreDenyList(store)
		if err == nil {
			var rules []policy.DenyRule
			if rules, err = deny.List(ctx); err == nil {
				return result(doctor.StatusOK, "", "policy profile %s; %d deny rule(s) readable", profile, len(rules))
			}
		}
		return result(doctor.StatusWarn, "restore the policy store or restart the daemon", "policy profile %s; the deny store is unreadable: %v", profile, err)
	}}
}

// newApprovalKeyDoctorCheck reports whether the approval signing key is stored
// and consistent, reading the vault without a probe or key file write (the
// validation of the unix-only approvalKeyStatus).
func newApprovalKeyDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return securityDoctorCheck{"approval_key", "daemon approval signing key presence", func(ctx context.Context) doctor.CheckResult {
		if paths.DataDir() == "" {
			return result(doctor.StatusWarn, "", "approval key: %s", errDoctorNoDataDir)
		}
		custody, err := doctorApprovalKeyCustody(hostCustodyConfig(paths.DataDir()))
		if err != nil {
			return result(doctor.StatusWarn, "", "approval key: the vault is unreadable: %v", err)
		}
		value, err := custody.Get(ctx, secrets.ApprovalKeyName)
		defer clear(value)
		switch {
		case cascade.HasKind(err, cascade.KindNotFound):
			return result(doctor.StatusOK, "", "approval key not stored yet: generated at first daemon run")
		case err != nil:
			return result(doctor.StatusWarn, "", "approval key: unreadable: %v", err)
		case len(value) != ed25519.PrivateKeySize:
			return result(doctor.StatusWarn, "", "approval key: the stored value is not an ed25519 private key")
		}
		derived := ed25519.NewKeyFromSeed(value[:ed25519.SeedSize])
		defer clear(derived)
		if !bytes.Equal(derived, value) {
			return result(doctor.StatusWarn, "", "approval key: the stored value is not a consistent ed25519 key pair")
		}
		return result(doctor.StatusOK, "", "approval key present")
	}}
}

// newEgressAuditOnlyDoctorCheck lists the egress classes inventoried without
// an enforced transit path, so the gap is visible rather than assumed closed.
func newEgressAuditOnlyDoctorCheck() doctor.Check {
	return securityDoctorCheck{"egress_audit_only", "egress classes without an enforced transit path", func(context.Context) doctor.CheckResult {
		var lines []string
		for _, class := range egress.AuditOnlyClasses() {
			reason, _ := egress.AuditOnly(class)
			lines = append(lines, fmt.Sprintf("%s: %s", class, reason))
		}
		res := result(doctor.StatusOK, "", "%d egress class(es) are audit-only", len(lines))
		res.Detail = strings.Join(lines, "\n")
		return res
	}}
}

// newCallerPrincipalDoctorCheck states what caller principals do and do not
// protect (ADR-0006 section 3). It reports no credential counts: they live
// only in the issuing daemon's memory, which doctor cannot read.
func newCallerPrincipalDoctorCheck() doctor.Check {
	return securityDoctorCheck{"caller_principal", "caller principal threat model", func(context.Context) doctor.CheckResult {
		return result(doctor.StatusOK, "", "%s", callerPrincipalThreatModel)
	}}
}
