// Purpose: the audit_chain doctor check and the read-only audit-store helpers
// the presence history and policy checks share (split from
// doctor_mounts_security.go for the 300-line cap).
// Inputs: the path provider. Outputs: doctor.Check and parsed enrolments.
// Constraints: the only store open is runtime.OpenReadOnlyStore.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (P1-SEC-12).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// presenceEnrolment is one non-failed audited presence enrolment.
type presenceEnrolment struct{ fingerprint, kind string }

// auditedPresenceEnrolments reads them from the audit log through the
// read-only store, oldest first. A failed one never replaced the key; an
// intent with no outcome is kept.
func auditedPresenceEnrolments(ctx context.Context, paths runtime.PathProvider) ([]presenceEnrolment, error) {
	store, closeStore, found, err := openDoctorStore(ctx, paths)
	if err != nil || !found {
		return nil, err
	}
	defer func() { _ = closeStore() }()
	// Verified first, so the raw rows are authentic (audit.New is reserved to audit_log.go).
	if verr := openAuditVerifier(store).Verify(ctx); verr != nil {
		return nil, verr
	}
	it, err := store.Scan(ctx, string(storage.DomainAudit), "rec:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var intents []audit.Record
	failed := map[string]bool{}
	for it.Next(ctx) {
		var rec audit.Record
		if jerr := json.Unmarshal(it.Value(), &rec); jerr != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, jerr, "doctor: an audit record is unreadable")
		}
		switch {
		case rec.Actor != presenceEnrolActor || rec.Action != presenceEnrolAction:
		case rec.EffectPhase == string(audit.EffectIntent):
			intents = append(intents, rec)
		case rec.EffectPhase == string(audit.EffectFailed):
			failed[rec.EffectKey] = true
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	var out []presenceEnrolment
	for _, rec := range intents {
		var body struct{ Fingerprint, Kind string }
		if jerr := json.Unmarshal(rec.Explain, &body); jerr != nil || body.Fingerprint == "" {
			return nil, cascade.New(cascade.KindIntegrity, fmt.Sprintf("audit record %d: the presence enrolment body is unreadable", rec.Seq))
		}
		if !failed[rec.EffectKey] {
			out = append(out, presenceEnrolment{fingerprint: body.Fingerprint, kind: body.Kind})
		}
	}
	return out, nil
}

// openDoctorStore opens <data dir>/cascade.db read-only. found is false when
// the file does not exist: nothing has ever run, which opens and creates
// nothing.
func openDoctorStore(ctx context.Context, paths runtime.PathProvider) (provider.Store, func() error, bool, error) {
	if paths.DataDir() == "" {
		return nil, nil, false, cascade.New(cascade.KindUnavailable, errDoctorNoDataDir)
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if _, err := os.Lstat(dbPath); os.IsNotExist(err) {
		return nil, nil, false, nil
	} else if err != nil {
		return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "doctor: audit store")
	}
	store, closeStore, err := runtime.OpenReadOnlyStore(ctx, dbPath)
	return store, closeStore, err == nil, err
}

// doctorBetweenOpenAndVerify lets a test run a writer in the window between
// the store open and the verification (nil in production).
var doctorBetweenOpenAndVerify func()

// storeSignature is the main file's size and mtime, to notice a writer that
// changed it between an immutable open and its verification.
func storeSignature(dbPath string) string {
	info, err := os.Stat(dbPath)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
}

// sidecarsPresent reports whether a -wal or -shm file exists beside dbPath.
func sidecarsPresent(dbPath string) bool {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			return true
		}
	}
	return false
}

// newAuditChainDoctorCheck verifies the audit hash chain, tail and effect index
// through the read-only store, also while a daemon holds it. An immutable=1
// open reads the main file with no lock, so a writer that starts and
// checkpoints between the open and the verification makes the verification
// fail on a half-changed file: when the file changed or a sidecar appeared
// meanwhile, the store is opened and verified once more (mode=ro now) before
// anything is reported as tampering. A failure that persists is an error.
func newAuditChainDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return securityDoctorCheck{"audit_chain", "audit log hash chain verification (read-only)", func(ctx context.Context) doctor.CheckResult {
		dbPath := filepath.Join(paths.DataDir(), "cascade.db")
		for attempt := 0; ; attempt++ {
			before := storeSignature(dbPath)
			store, closeStore, found, err := openDoctorStore(ctx, paths)
			switch {
			case runtime.IsStoreInTransition(err):
				return result(doctor.StatusWarn, "run cascade doctor again; if it persists, start the daemon once", "audit chain not verified: %v", err)
			case err != nil:
				return result(doctor.StatusError, "restore the audit store or restart the daemon", "audit chain: the store could not be opened read-only: %v", err)
			case !found:
				return result(doctor.StatusOK, "", "no audit log yet")
			}
			if doctorBetweenOpenAndVerify != nil && attempt == 0 {
				doctorBetweenOpenAndVerify()
			}
			verr := openAuditVerifier(store).Verify(ctx)
			_ = closeStore()
			if verr == nil {
				return result(doctor.StatusOK, "", "audit chain verified")
			}
			if attempt == 0 && (storeSignature(dbPath) != before || sidecarsPresent(dbPath)) {
				continue
			}
			return result(doctor.StatusError, "treat the audit log as tampered: preserve cascade.db and investigate", "audit chain verification failed: %v", verr)
		}
	}}
}
