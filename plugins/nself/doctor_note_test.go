package nself

import "testing"

// TestDoctorNotesUserSetProfile pins the SOFT-DEFAULT behaviour
// (P1-E25-W5-S103-T1): a handshake result that skipped runtime.profile as
// user-set produces the note; every other result produces none.
func TestDoctorNotesUserSetProfile(t *testing.T) {
	skipped := ConfigResult{Skipped: []ConfigOutcome{{Path: "runtime.profile", Reason: "user-set"}}}
	if got := doctorNoteForHandshake(skipped); got != userSetProfileNote {
		t.Fatalf("doctorNoteForHandshake(skipped runtime.profile) = %q, want %q", got, userSetProfileNote)
	}

	otherSkip := ConfigResult{Skipped: []ConfigOutcome{{Path: "plugins.cascade-nself.postgres_host", Reason: "user-set"}}}
	if got := doctorNoteForHandshake(otherSkip); got != "" {
		t.Fatalf("doctorNoteForHandshake(unrelated skip) = %q, want empty", got)
	}

	applied := ConfigResult{Applied: []ConfigOutcome{{Path: "runtime.profile", Reason: "absent"}}}
	if got := doctorNoteForHandshake(applied); got != "" {
		t.Fatalf("doctorNoteForHandshake(applied runtime.profile) = %q, want empty (only a SKIP is user-set)", got)
	}

	if got := doctorNoteForHandshake(ConfigResult{}); got != "" {
		t.Fatalf("doctorNoteForHandshake(empty result) = %q, want empty", got)
	}
}
