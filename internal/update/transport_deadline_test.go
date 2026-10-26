package update

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGetBoundsStalledBody: a pinned peer that sends headers and part of the
// body and then stalls is cut off by the request deadline; the archive
// download refuses instead of hanging.
func TestGetBoundsStalledBody(t *testing.T) {
	orig := requestDeadline
	t.Cleanup(func() { requestDeadline = orig })
	requestDeadline = 3 * time.Second
	base, log := startRelease(t, t.TempDir(), "v2.0.1")
	log.mu.Lock()
	log.stall = "/releases/download/v2.0.1/stall"
	log.mu.Unlock()
	fetch := HTTPFetcher{Interceptor: &countingInterceptor{}, BaseURL: base}
	start := time.Now()
	rc, err := fetch.Get(context.Background(), base+"/download/v2.0.1/stall")
	must(t, err)
	n, err := io.Copy(io.Discard, rc)
	_ = rc.Close()
	if err == nil || n != 64<<10 || time.Since(start) > 30*time.Second {
		t.Fatalf("stalled body: read %d bytes, err %v after %s; want the 64 KiB then a deadline error", n, err, time.Since(start))
	}
	err = downloadVerified(context.Background(), fetch, base+"/download/v2.0.1/stall", filepath.Join(t.TempDir(), "archive"), hexSum(nil))
	wantKind(t, err, cascade.KindUnavailable, "update: download")
}
