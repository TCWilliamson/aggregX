// Package ingest is the hot-path run mode (spec 03 §6, spec 06 §3).
//
// Phase 0 skeleton: request parsing and envelope/COSE structural validation are
// wired to the real wire package. Identity (directory), the Redis round trip,
// Redpanda produce and reply signing are TODO and return 501 so nothing can be
// mistaken for an acknowledgement.
package ingest

import (
	"io"
	"mime"
	"net/http"

	"iotp/internal/wire"
)

// Server is the ingest HTTP front end for one cell.
type Server struct {
	Cell string
}

// Register adds the device and gateway ingress routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ingest", s.direct)
	mux.HandleFunc("POST /v1/gw/relay", s.relay)
}

// direct handles POST /v1/ingest from direct-HTTPS devices (spec 01 §9).
// The body is a COSE_Sign1 signed batch (spec 03 §4).
func (s *Server) direct(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, wire.MaxDirectEnvelope+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > wire.MaxDirectEnvelope {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !isCOSESign1(r.Header.Get("Content-Type")) {
		http.Error(w, "content type", http.StatusUnsupportedMediaType)
		return
	}
	msg, err := wire.ParseSign1(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	// TODO(phase0): find the device key by msg.KID in the directory and verify
	// (spec 02 §3.1) before trusting anything in the payload.
	if _, err := wire.DecodeHeader(msg.Payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// TODO(phase0): directory status, Redis round trip, produce, mark, signed reply.
	http.Error(w, "ingest pipeline not implemented", http.StatusNotImplemented)
}

// isCOSESign1 reports whether ct is application/cose; cose-type="cose-sign1" (spec 01 §1).
func isCOSESign1(ct string) bool {
	mt, params, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/cose" && params["cose-type"] == "cose-sign1"
}

// relay handles POST /v1/gw/relay from gateways (spec 02 §4).
func (s *Server) relay(w http.ResponseWriter, r *http.Request) {
	gw := r.Header.Get("X-Client-Identity") // set by HAProxy after mTLS
	if gw == "" {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	req, err := wire.DecodeRelayRequest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.GW != gw {
		http.Error(w, "gw does not match certificate", http.StatusBadRequest)
		return
	}
	// Structural checks only, per item, so the relay path can be exercised end to end.
	for _, item := range req.Items {
		if _, err := wire.ParseSign1(item); err != nil {
			continue // would be code 8 malformed
		}
		// TODO(phase0): look up device keys in the directory, verify (spec 02 §3.1).
	}
	http.Error(w, "relay pipeline not implemented", http.StatusNotImplemented)
}
