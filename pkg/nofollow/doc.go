// Package nofollow exports the open flags that refuse a symlink as the final
// path component and keep an open of a FIFO from blocking, so a caller can
// pass them to os.OpenFile without naming syscall itself.
//
// Purpose: the platform open flags a tree scanner or a private-file writer
//
//	needs: NoFollow (O_NOFOLLOW) and NonBlock (O_NONBLOCK).
//
// Inputs: none.
// Outputs: two int constants to OR into an os.OpenFile flag set.
// Constraints: imports syscall only. On windows both are 0: windows has
//
//	no O_NOFOLLOW or FIFO, so a caller there must not rely on either
//	guard and should refuse the operation instead (the claude agent
//	driver never spawns on windows).
//
// SPORT: pkg/nofollow (ADD) — P1-AGT-01.
package nofollow
