// Purpose: prove the real client-construction failure drops the raw cause.
// Inputs: an ambiguous endpoint with a split canary; no network is reached.
// Outputs: assertions on the returned Kind and every Unwrap message.
// Constraints: untagged, no credentials or endpoint values in failure logs.
// SPORT: providers.s3.
package s3

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestS3NewErrorDropsMinioCause(t *testing.T) {
	c := s3Canary()
	accessKey, secretKey := c+"-key", c+"-secret"
	raw := "http://:47113#" + c + "@host"
	host, secure, err := parseEndpoint(raw)
	if err != nil {
		t.Fatal("fixture must reach client construction")
	}
	_, cause := minio.New(host, &minio.Options{Secure: secure})
	if cause == nil || !strings.Contains(cause.Error(), "47113") {
		t.Fatal("fixture must expose a password fragment in the minio error")
	}
	conn, err := Open(context.Background(), raw, "bucket", accessKey, secretKey)
	if conn != nil || err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatal("client construction must return KindInvalidInput and no connection")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if countHits(e.Error(), []string{c, "47113", raw, accessKey, secretKey}) != 0 {
			t.Error("client construction error chain contains a password fragment")
		}
	}
	if errors.Unwrap(err) != nil {
		t.Error("client construction retained its raw cause")
	}
}

func TestS3OpenErrorDetachesCredentials(t *testing.T) {
	c := s3Canary()
	accessKey, secretKey := c+"-key", c+"-secret"
	endpoint := "http://u:" + c + "@127.0.0.1:99999/?token=" + c
	probe := strings.Join([]string{endpoint, accessKey, secretKey}, "|")
	wrapped := wrapConnError(errors.New(probe), endpoint, "s3.Open: probe")
	if countHits(wrapped.Error(), []string{c, accessKey, secretKey, endpoint}) != 0 || errors.Unwrap(wrapped) != nil {
		t.Fatal("connection wrapper must redact the endpoint and detach the minio error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Open(ctx, endpoint, "bucket", accessKey, secretKey)
	if conn != nil || err == nil {
		t.Fatal("unreachable S3 endpoint must return an error and no connection")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatal("unreachable S3 endpoint must retain its classified connection Kind")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if countHits(e.Error(), []string{c, accessKey, secretKey, endpoint}) != 0 {
			t.Error("S3 open error chain contains endpoint or credential text")
		}
	}
	if errors.Unwrap(err) != nil {
		t.Error("S3 open error retained its minio cause")
	}
}
