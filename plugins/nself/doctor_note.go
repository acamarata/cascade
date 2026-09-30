package nself

// userSetProfileNote is the note a handshake APPLY emits when
// runtime.profile was left unchanged because the operator already set it
// (P1-E25-W5-S103-T1's SOFT-DEFAULT: overridable = true, a user edit is
// never rewritten).
const userSetProfileNote = "runtime.profile is user-set; cascade-nself left it unchanged"

// doctorNoteForHandshake returns userSetProfileNote when result skipped
// runtime.profile as user-set, else "".
func doctorNoteForHandshake(result ConfigResult) string {
	for _, o := range result.Skipped {
		if o.Path == "runtime.profile" && o.Reason == "user-set" {
			return userSetProfileNote
		}
	}
	return ""
}
