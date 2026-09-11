// Package targets holds the backup repository's Target drivers
// (05-PEWS-PLAN-W4-W6 §Epic S S-41.T3): fs (a local filesystem root), s3
// (the real S3 API, a standalone client this package owns per §D-15), and
// rclone (exec-only against the rclone binary, the NAS/B2/WebDAV/Drive
// universe behind one tool). Each implements internal/backup's Target
// interface (Put/Get/List/Delete) by duck typing only — this package
// never imports internal/backup, so internal/backup's repo.go stays the
// single definition of the interface these drivers satisfy.
//
// SPORT: internal.backup.targets/ADDED (P1-E19-W4-S41-T3).
package targets
