# IoT platform

Self-hosted, multi-tenant IoT platform for ESP32 and Nordic devices (Thread through our own border routers, plus WiFi), over HTTPS. One Go codebase with three run modes (`ingest`, `core`, `workers`), ClickHouse for telemetry, PostgreSQL for metadata, Redpanda as the stream buffer, Redis for hot state.

**Status: Phase 0 foundations.** The specs are written and the wire format is implemented and tested. The run modes start, connect to every service and report readiness, but the ingest pipeline, workers and simulator device model are not built yet: ingest answers `501` rather than acknowledging anything.

## Start here

- [`docs/specs/`](docs/specs/README.md): the contracts everything is built against. Read [`CONTRADICTIONS.md`](docs/specs/CONTRADICTIONS.md) for design-doc issues and the open decisions.
- [`spec/wire/envelope.cddl`](spec/wire/envelope.cddl): the normative wire grammar.
- [`spec/vectors/`](spec/vectors/): golden test vectors shared by Go, C and the simulator.

## Layout

| Path | What |
|---|---|
| `cmd/platform` | The one binary: `platform ingest|core|workers|migrate` |
| `cmd/simulator` | Device and gateway simulator (smoke test only so far) |
| `cmd/iotgen` | Schema checker and Go/C constant generator |
| `internal/wire` | Envelope, reply, COSE_Sign1 profile, relay protocol (specs 01, 02) |
| `internal/ingest` | Ingest run mode (skeleton) |
| `internal/migrate`, `migrations/` | PostgreSQL and ClickHouse migrations (specs 05, 06) |
| `spec/schemas/` | Example product and module schemas (spec 04) |
| `sdk/c/` | C side: generated schema headers, zcbor vector test |
| `deploy/compose/` | Development stack: HAProxy, Redpanda, PostgreSQL, ClickHouse, Redis, Keycloak, S3 store |
| `scripts/` | Dev certificates, CDDL validation, C codec check |

## Running it on your PC

Prerequisites: Docker with Compose v2, Go 1.26, `make`. Optional for the C and CDDL checks: Python 3, `pip install zcbor 'cbor2<5.7'`, gcc.

```sh
make test                 # Go unit tests and golden vectors
make up                   # build the image and start the stack (first run pulls ~3 GB of images)
make smoke                # 50 simulated devices -> gateway mTLS -> HAProxy -> ingest; expects HTTP 501
make down                 # stop (data kept in named volumes); make clean deletes it
```

Faster rebuilds while developing: `make build-linux` then `IOTP_DOCKERFILE=Dockerfile.local make up` (copies host binaries instead of compiling in Docker).

Ports: `8443` device ingress (TLS; gateway path needs the dev client certificate), `8080` API, `8404` HAProxy stats, `8180` Keycloak (admin/admin), `8333` S3.

Dev certificates and keys are generated into the `certs` volume on first start by `scripts/dev-keys.sh`. They are for development only.

### Checked on a throwaway cloud machine (14 Nov 2026)

- `go vet`, `go build`, `go test ./...` pass; golden vectors validate against the CDDL (`make cddl`); the zcbor-generated C codec decodes every vector (`make c-vectors`).
- The full stack came up; both PostgreSQL migrations and the ClickHouse tables applied; the Redpanda topic was created with 48 partitions; every run mode's `/readyz` reported all dependencies ok.
- `make smoke` passed: 50 signed batches through gateway mTLS reached ingest and were decoded. Requests to the gateway path without a client certificate, or with a forged identity header, got 401.

Two things differed on that machine and should not be needed on a PC: ClickHouse's raised file limit (`ulimits`) was not permitted, so it ran without it; and containers had no direct internet, so the dev certificates were generated on the host.
