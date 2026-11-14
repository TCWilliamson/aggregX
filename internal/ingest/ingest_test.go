package ingest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"iotp/internal/wire"
)

func do(t *testing.T, path string, hdr map[string]string, body []byte) int {
	t.Helper()
	mux := http.NewServeMux()
	(&Server{Cell: "uk1"}).Register(mux)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func relay(t *testing.T, gw string) []byte {
	t.Helper()
	b, err := wire.Marshal(wire.RelayRequest{V: 1, GW: gw})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func relayWithItems(t *testing.T) []byte {
	t.Helper()
	payload, err := wire.ExampleMinimal().Encode()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := wire.Sign1(wire.TestKey(1), payload, wire.AADBatch)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wire.Marshal(wire.RelayRequest{V: 1, GW: "GW-00001", Items: [][]byte{signed, {0x00}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// spec 02 §4: gateway relay front-door checks. Until the pipeline exists every
// accepted request must answer 501, never anything that could read as an ack.
func TestRelayFrontDoor(t *testing.T) {
	id := map[string]string{"X-Client-Identity": "GW-00001"}
	cases := []struct {
		name string
		hdr  map[string]string
		body []byte
		want int
	}{
		{"no client identity", nil, relay(t, "GW-00001"), http.StatusUnauthorized},
		{"too large", id, make([]byte, 1<<20+1), http.StatusRequestEntityTooLarge},
		{"not cbor", id, []byte("hello"), http.StatusBadRequest},
		{"gw mismatch", id, relay(t, "GW-00002"), http.StatusBadRequest},
		{"valid", id, relay(t, "GW-00001"), http.StatusNotImplemented},
		{"items, one malformed", id, relayWithItems(t), http.StatusNotImplemented},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := do(t, "/v1/gw/relay", c.hdr, c.body); got != c.want {
				t.Fatalf("want %d, got %d", c.want, got)
			}
		})
	}
}

const coseType = `application/cose; cose-type="cose-sign1"`

// spec 01 §1, §9 and spec 03 §4: a direct upload is a signed batch. Until the
// pipeline exists every accepted request must answer 501, never an ack.
func TestDirectFrontDoor(t *testing.T) {
	payload, err := wire.ExampleMinimal().Encode()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := wire.Sign1(wire.TestKey(1), payload, wire.AADBatch)
	if err != nil {
		t.Fatal(err)
	}
	cose := map[string]string{"Content-Type": coseType}
	cases := []struct {
		name string
		hdr  map[string]string
		body []byte
		want int
	}{
		{"too large", cose, make([]byte, wire.MaxDirectEnvelope+1), http.StatusRequestEntityTooLarge},
		{"unsigned envelope as cbor", map[string]string{"Content-Type": "application/cbor"}, payload, http.StatusUnsupportedMediaType},
		{"cose without cose-type", map[string]string{"Content-Type": "application/cose"}, signed, http.StatusUnsupportedMediaType},
		{"unsigned envelope labelled cose", cose, payload, http.StatusUnauthorized},
		{"signed, bad envelope inside", cose, signedPayload(t, []byte{0xA0}), http.StatusBadRequest},
		{"signed", cose, signed, http.StatusNotImplemented},
		{"signed, quoted param spacing", map[string]string{"Content-Type": `application/cose;cose-type=cose-sign1`}, signed, http.StatusNotImplemented},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := do(t, "/v1/ingest", c.hdr, c.body); got != c.want {
				t.Fatalf("want %d, got %d", c.want, got)
			}
		})
	}
}

func signedPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	b, err := wire.Sign1(wire.TestKey(1), payload, wire.AADBatch)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
