//go:build !windows

package main

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var _ = rpc.DeclareTopicAccess("jobs.evidence", rpc.AccessOwnerOnly)

type jobsEvidenceSSE struct{ bus *events.Bus }

func evidenceStreamError(w http.ResponseWriter, status int, kind cascade.Kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpc.NewEnvelope(nil, nil, &rpc.ErrorObject{
		Code: kind.JSONRPCCode(), Message: message, Data: map[string]string{"kind": kind.String()},
	}))
}

func (h *jobsEvidenceSSE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	values := query["ticket"]
	if err != nil || len(values) != 1 || values[0] == "" {
		evidenceStreamError(w, 400, cascade.KindInvalidInput, "evidence: exactly one non-empty ticket required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		evidenceStreamError(w, 500, cascade.KindInternal, "evidence: streaming unavailable")
		return
	}
	var nonce [16]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		evidenceStreamError(w, 500, cascade.KindInternal, "evidence: cursor unavailable")
		return
	}
	cursor := "evidence-sse:" + hex.EncodeToString(nonce[:])
	if err := h.bus.ResetCursorToHead(r.Context(), jobs.EvidenceNamespace, cursor); err != nil {
		evidenceStreamError(w, 503, cascade.KindUnavailable, "evidence: subscription unavailable")
		return
	}
	sub, err := h.bus.Subscribe(r.Context(), jobs.EvidenceNamespace, cursor, 64)
	if err != nil {
		evidenceStreamError(w, 503, cascade.KindUnavailable, "evidence: subscription unavailable")
		return
	}
	defer func() { _ = sub.Unsubscribe() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = fmt.Fprint(w, ": subscribed\n\n")
	flusher.Flush()
	h.stream(w, r, flusher, sub, values[0])
}

func (h *jobsEvidenceSSE) stream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, sub *events.Subscription, ticket string) {
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			var changed jobs.EvidenceChanged
			if ev.Kind != jobs.EventEvidenceChanged {
				continue
			}
			if json.Unmarshal(ev.Payload, &changed) != nil || changed.JobID == "" {
				return
			}
			if changed.JobID != ticket {
				continue
			}
			class, err := provider.ParseDataClassName(changed.DataClass)
			if err != nil {
				class = provider.SensitivityLocalOnly
			}
			changed.DataClass, _ = provider.DataClassName(class)
			_, redacted := jobs.RedactForCeiling(jobs.EvidenceRecord{DataClass: &class}, jobs.EvidenceReadCeiling)
			payload, err := json.Marshal(struct {
				jobs.EvidenceChanged
				Redacted bool `json:"redacted"`
			}{changed, redacted})
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", ev.Kind, ev.Seq, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
