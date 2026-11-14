// Command simulator impersonates devices and gateways (spec 07 §6).
//
// Phase 0 skeleton: "smoke" mode builds one signed batch per simulated device,
// wraps them in a gateway relay request and posts it through HAProxy with the
// dev gateway certificate, proving TLS, mTLS, routing and ingest parsing.
// The device model, fault injector and journal are the Phase 0 build.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"iotp/internal/wire"
)

func main() {
	target := flag.String("target", "https://haproxy:8443", "device ingress base URL")
	certs := flag.String("certs", "/certs", "directory with dev certificates")
	gw := flag.String("gw", "GW-00001", "gateway serial (must match the dev gateway certificate CN)")
	n := flag.Int("devices", 10, "devices behind the gateway")
	flag.Parse()
	if err := smoke(*target, *certs, *gw, *n); err != nil {
		log.Fatal(err)
	}
}

// smoke posts one relay request carrying n signed batches and expects 501, the
// Phase 0 skeleton's answer: the request passed TLS, mTLS, HAProxy routing, the
// identity header and relay decoding.
func smoke(target, certs, gw string, n int) error {
	client, err := gatewayClient(certs, gw)
	if err != nil {
		return err
	}
	body, err := relayBody(gw, n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target+"/v1/gw/relay", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post relay: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response body; nothing to recover on close.
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}
	fmt.Printf("relay: %d devices, %d bytes -> HTTP %d: %s", n, len(body), resp.StatusCode, out)
	if resp.StatusCode != http.StatusNotImplemented {
		return fmt.Errorf("want HTTP 501 from the Phase 0 skeleton, got %d", resp.StatusCode)
	}
	return nil
}

// gatewayClient builds an mTLS client presenting the dev certificate for gw.
func gatewayClient(certs, gw string) (*http.Client, error) {
	ca, err := os.ReadFile(filepath.Join(certs, "ingress-ca.pem")) //nolint:gosec // G304: dev cert dir from the command line.
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("ingress-ca.pem holds no certificates")
	}
	base := filepath.Join(certs, "gw-"+gw)
	cert, err := tls.LoadX509KeyPair(base+".pem", base+".key")
	if err != nil {
		return nil, fmt.Errorf("load gateway cert: %w", err)
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: pool, Certificates: []tls.Certificate{cert},
			ServerName: "ingest.uk1.iotp.test", MinVersion: tls.VersionTLS12,
		},
	}}, nil
}

// relayBody builds a relay request with one signed single-record batch per device.
func relayBody(gw string, n int) ([]byte, error) {
	if n < 1 || n > wire.MaxRelayItems {
		return nil, fmt.Errorf("devices must be 1..%d", wire.MaxRelayItems)
	}
	req := wire.RelayRequest{V: 1, GW: gw, Items: make([][]byte, 0, n)}
	now := uint64(time.Now().UnixMilli()) //nolint:gosec // G115: wall-clock milliseconds are positive.
	for i := range n {
		e := &wire.Envelope{
			V: 1, Dev: fmt.Sprintf("SN-SIM%05d", i), Inc: 1000 + uint32(i), Boot: 1, //nolint:gosec // G115: i < MaxRelayItems.
			Schema: "envsense@3.2", FW: "0.0.1", Cfg: make([]byte, 8), TB: wire.TBSynced, T0: now,
			R: &wire.RecordSection{First: 0, Records: []wire.Record{{Dt: 0, Values: map[uint16]any{1: float32(20.5)}}}},
		}
		payload, err := e.Encode()
		if err != nil {
			return nil, fmt.Errorf("encode device %d: %w", i, err)
		}
		signed, err := wire.Sign1(wire.TestKey(int64(i)), payload, wire.AADBatch)
		if err != nil {
			return nil, fmt.Errorf("sign device %d: %w", i, err)
		}
		req.Items = append(req.Items, signed)
	}
	body, err := wire.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("encode relay: %w", err)
	}
	return body, nil
}
