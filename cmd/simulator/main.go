// Command simulator impersonates devices and gateways (spec 07 §6).
//
// Phase 0 skeleton: "smoke" mode builds one signed batch per simulated device,
// wraps them in a gateway relay request and posts it through HAProxy with the
// dev gateway certificate, proving TLS, mTLS, routing and ingest parsing.
// The device model, fault injector and journal are the Phase 0 build.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"iotp/internal/wire"
)

func main() {
	target := flag.String("target", "https://haproxy:8443", "device ingress base URL")
	certs := flag.String("certs", "/certs", "directory with dev certificates")
	gw := flag.String("gw", "GW-00001", "gateway serial (must match the dev gateway certificate CN)")
	n := flag.Int("devices", 10, "devices behind the gateway")
	flag.Parse()

	ca, err := os.ReadFile(*certs + "/ingress-ca.pem")
	if err != nil {
		log.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	cert, err := tls.LoadX509KeyPair(*certs+"/gw-GW-00001.pem", *certs+"/gw-GW-00001.key")
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: "ingest.uk1.iotp.test"},
	}}

	req := wire.RelayRequest{V: 1, GW: *gw}
	now := uint64(time.Now().UnixMilli())
	for i := 0; i < *n; i++ {
		e := &wire.Envelope{
			V: 1, Dev: fmt.Sprintf("SN-SIM%05d", i), Inc: 1000 + uint32(i), Boot: 1,
			Schema: "envsense@3.2", FW: "0.0.1", Cfg: make([]byte, 8), TB: wire.TBSynced, T0: now,
			R: &wire.RecordSection{First: 0, Records: []wire.Record{{Dt: 0, Values: map[uint16]any{1: float32(20.5)}}}},
		}
		payload, err := e.Encode()
		if err != nil {
			log.Fatal(err)
		}
		signed, err := wire.Sign1(wire.TestKey(int64(i)), payload, wire.AADBatch)
		if err != nil {
			log.Fatal(err)
		}
		req.Items = append(req.Items, signed)
	}
	body, err := wire.Marshal(&req)
	if err != nil {
		log.Fatal(err)
	}
	resp, err := client.Post(*target+"/v1/gw/relay", "application/cbor", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("relay: %d devices, %d bytes -> HTTP %d: %s", *n, len(body), resp.StatusCode, out)
	// 501 is the expected answer from the Phase 0 skeleton: it means the request
	// passed TLS, mTLS, HAProxy routing, identity header and relay decoding.
	if resp.StatusCode != http.StatusNotImplemented {
		os.Exit(1)
	}
}
