# 03. Identity, claiming, tokens and revocation

Status: Draft 1. Depends on spec 01 and 02.

**Principle:** identity comes from the device, tenancy from the server. Every device and gateway has its own key, generated at manufacture. No secret is shared between devices. Which org a device belongs to is a fact in the platform's database, never something the device asserts.

The "company or project token" idea from the original brief survives in two places: as a **claim code** used once at installation (§3), and as the routing claims inside the short-lived tokens issued to direct devices (§4).

## 1. Identities

| Identity | Credential | Proves itself by | Lifetime |
|---|---|---|---|
| Device (Thread) | P-256 factory key pair, private key on the device | Signing every batch (spec 02 §3) | Life of the device; rotatable Later |
| Device (direct) | Same factory key | Signing a challenge to get a JWT (§4) | JWT 1–2 h |
| Gateway | P-256 key + X.509 client certificate | Mutual TLS (§5) | Certificate 90 days, auto-renewed |
| Factory jig | X.509 client certificate from the Factory CA | Mutual TLS to the registration API | Per production run, ≤ 30 days |
| Platform user | Identity provider session (OIDC) | Login, MFA | IdP-managed |
| API client | Org API key | `Authorization: Bearer iotp_…` | Until revoked |

User and API-key authentication are specified with the API in Phase 1; this spec covers devices, gateways and the platform's own keys.

## 2. Factory identity

### 2.1 Serials

- Devices: `SN-` + 5–12 uppercase alphanumerics, e.g. `SN-00042`. Gateways: `GW-…`. Modules: `M-…`. Allowed characters `[A-Z0-9-]`, ≤ 32 chars.
- Serials are allocated by the platform per production batch and are never reused.

### 2.2 Key generation and registration

1. The jig flashes the production firmware and enables the part's protections (secure boot, flash encryption or readback protection, debug lock). These steps are irreversible and are rehearsed on sacrificial units.
2. The device generates its P-256 key pair internally. The private key never leaves the device.
3. The jig reads back the serial and the 65-byte uncompressed public key.
4. The jig registers the device:

```
POST /v1/factory/registrations          (mTLS, jig certificate)
{ "batch": "B-2026-11-003", "serial": "SN-00042", "hw_rev": "envsense-r3",
  "public_key": "<base64url 65 bytes>", "alg": "ES256", "fw": "2.4.1",
  "claim_code_hash": "<base64url SHA-256 of the claim code>" }
```

5. The jig writes into the device: the cell's ingest hostname, the device-ingress server CA (§7), the current and next cloud **reply public keys** (spec 02 §5.2), and prints the label with the claim code (§3.1).

A jig certificate is bound to one batch manifest. Registrations for serials outside that manifest are refused. A registered device has status `registered` and belongs to no org.

### 2.3 Private key protection by part

The pilot part is a Phase 0 hardware decision. What each candidate offers:

| Part | Where the private key lives | Notes |
|---|---|---|
| nRF52840 | Internal flash, protected by APPROTECT readback protection | CryptoCell-310 accelerates P-256 but stores no keys. Weakest of the options. |
| nRF5340 / nRF54L (incl. nRF54L15 in the PAN B511-1C) | Hardware key storage (KMU) with TF-M | nRF54L also has more flash, which helps the MCUboot slot budget. **Proposed pilot device** (CONTRADICTIONS C28). |
| ESP32-H2 | ECDSA peripheral with key in eFuse | Key unreadable by software. |
| ESP32-C6, -S3, -C3 | Encrypted flash (flash encryption + secure boot) | The DS peripheral on these parts is RSA-only and cannot hold a P-256 key. |

> **Design note.** The choice of ES256 (spec 02 §2) is the one compatible with every row of this table. Ed25519 would rule out the hardware peripherals on the H2 and the KMU-backed path on Nordic parts.

### 2.4 Device keys

A device has one or more keys, each with `kid` (spec 02 §3), algorithm, public key and status: `active` (signs and verifies), `retiring` (verifies only, during a rotation), `revoked`. Pilot devices have exactly one key for life. Key rotation on the device is Later; the `kid` field and status column are its hooks.

## 3. Claiming

Claiming assigns a registered device to an org, product and site, and opens its first data stream (spec 05 §4).

### 3.1 Claim codes

- Each device gets a random **claim code** at manufacture: 80 bits from a CSPRNG, written as 16 Crockford base32 characters in groups of four, e.g. `K7Q2-M9XD-4TPA-8RWE`.
- The label carries a QR code of `IOTP1:<serial>:<claim code>`.
- The platform stores only `SHA-256(claim code)`. With 80 bits of entropy a pepper adds nothing.
- A claim code works only while the device is **unclaimed**. After an unclaim (RMA, resale) the same label works again for the next owner; there is no need to relabel.

### 3.2 Claim by a user (Pilot)

The installer, signed in to the portal or installer app as a user with the `assign` capability in the target org, scans the label:

```
POST /v1/claims
{ "serial": "SN-00042", "claim_code": "K7Q2-M9XD-4TPA-8RWE",
  "org": "org_7Hq…", "site": "site_2Lm…" }
```

The platform checks the code, the device status (`registered` or `unclaimed`), and that the device's hardware revision belongs to a product the org may use. In one transaction it sets the device's org and product, opens a stream, records the claim in the audit log, and bumps the device's credential version (which publishes it to ingest, §8).

Failed attempts are limited to 5 per serial per hour and 20 per user per hour.

### 3.3 Pre-assignment by batch (Pilot)

A production batch can be pre-assigned to an org and site in its manifest. Devices in it are claimed at registration time, without a label scan. This is the path for devices built for a known customer order.

### 3.4 Order tokens (Pilot)

An **order token** is the multi-device form of a claim code: a random code bound to an org, optionally a site, a list of serials or a batch, a use limit and an expiry. An installer types or scans it once and claims every device in scope. Stored hashed, revocable.

### 3.5 Device-presented claim (Later)

For self-installed products the device itself could present an order token on first contact. The hook is the `claim_tokens` table and a reserved claim endpoint; nothing is built in the pilot.

### 3.6 Before a device is claimed

Ingest answers batches from a registered but unclaimed device with `st = 2` (spec 01) or code 4 (spec 02), no acks, and a long `next`. Nothing is stored. The device keeps its outbox, so readings taken during installation are not lost if the claim completes before the outbox fills.

## 4. Device tokens (direct path)

Specified now; built when the first direct (WiFi) product is (see CONTRADICTIONS.md C10).

### 4.1 Getting a token

```
POST /v1/device-auth/challenge            → { "nonce": "<opaque>", "exp": 1794614960 }
POST /v1/device-auth/token                  body: COSE_Sign1 (device key, AAD "iotp-auth-v1")
                                            payload: {0: dev, 1: nonce, 2: inc}
                                          → { "token": "<JWT>", "exp": 1794621800 }
```

- The nonce is single-use (recorded in Redis for its 60 s life), so a captured token request cannot be replayed.
- Devices with an X.509 certificate MAY authenticate by mutual TLS instead (Later).

### 4.2 Token format

JWS compact, header `{"alg": "ES256", "kid": "<key id>", "typ": "JWT"}`.

```json
{ "iss": "https://auth.uk1.example", "aud": "ingest.uk1", "sub": "dev:SN-00042",
  "iat": 1794614900, "nbf": 1794614900, "exp": 1794621800,
  "dev": "SN-00042", "cell": "uk1", "prod": "envsense", "tier": "standard", "ver": 3 }
```

| Claim | Meaning |
|---|---|
| `dev` | Device serial. The envelope's `dev` must equal it. |
| `cell` | The cell that issued it. Ingest rejects tokens for another cell. |
| `prod` | Product key. Informational for ingest; workers use the registry. |
| `tier` | Allowance tier, so ingest can find the device's allowance without a lookup. |
| `ver` | The device's credential version when the token was issued (§8). |

- Lifetime: uniformly random between 3,600 and 7,200 s, so a fleet never expires at once.
- Not in the token: org, groups, schema version, **stream**. Org and groups change and ingest does not need them; the schema comes from the envelope.

> **Design note (C9).** The design doc's token includes `stream`. Routing a sample to a stream must depend on its sequence number, not on the token in force when it was uploaded (isolated moves, spec 05 §5). A `stream` claim invites exactly the routing bug the design doc warns against, and nothing needs it. This spec leaves it out.

### 4.3 Validation at ingest

Algorithm pinned to ES256 (the header's `alg` is checked, never trusted); `kid` is a current token key from core's JWKS (cached, refreshed every 5 minutes and at most once a minute on an unknown `kid`); `iss`, `aud`, `cell` match; `nbf` and `exp` valid with 60 s leeway; `ver` ≥ the device's current credential version; the device is `active`. A verified token is cached per pod by its SHA-256 until `exp`.

## 5. Gateway certificates

- X.509 v3, ES256, issued by the **Gateway CA** (an online intermediate, §9).
- Subject CN = gateway serial. SAN URI `urn:iotp:<cell>:gw:<serial>`. Extended key usage `clientAuth` only.
- Validity 90 days. The gateway renews from day 60 with `POST /v1/gw/cert` over its current mTLS session, sending a CSR for a fresh key. The first certificate is issued at the factory.
- HAProxy checks the chain. Ingest checks the gateway's status and credential version in the directory (§8) on every request, so revoking a gateway takes effect without CRLs or OCSP.
- Gateways are claimed to an org and site exactly like devices (§3).

## 6. Ingest checks, in order

Cheapest first; the first failure ends the request.

| # | Check | Failure |
|---|---|---|
| 1 | Body size within the path's limit | 413 |
| 2 | Content type; strict CBOR decode of the outer structure | 415 / 400 |
| 3 | **Identity.** Direct: JWT (§4.3). Gateway: `X-Client-Identity` present, gateway active in the directory, `gw` matches; then per batch, COSE verification (spec 02 §3.1) | 401 / 403 / per-item code 2 |
| 4 | Envelope header (spec 01 §6). `dev` equals the token's `dev` or the signing key's device | 400 / code 8 |
| 5 | Device status from the directory: claimed and not revoked | 403 / codes 3–5 |
| 6 | **One pipelined Redis round trip** per device: sequence state for each series in the batch (spec 06 §3), allowance (§6.1), pending mailbox count | — |
| 7 | Allowance exceeded | 429 / code 6 |
| 8 | If the batch has any sequence number not already accepted: produce to Redpanda, wait for the acknowledgement (acks=all) | 503 / code 7 |
| 9 | Mark sequence numbers accepted in Redis; build the reply (acks, resends, up to 8 mailbox items); sign it on the gateway path | — |

Ingest never decodes values or validates against the schema registry; workers do (spec 04 §7).

### 6.1 Allowances

A token bucket per device identity, held in Redis and checked in the round trip of step 6:

- **Request rate**: capacity `burst` requests, refilling at `rate` per minute.
- **Daily volume**: samples and bytes per UTC day, reset at midnight.
- Values come from the device's tier (product plan × power profile). Gateways have their own bucket for their own traffic and request rate. Per-org buckets sit on top (Phase 3).
- An over-allowance batch is not stored and not acknowledged; the reply has `st = 1` and a `next` long enough to bring the device back under its rate. The device keeps the data.
- Limits are per device or gateway identity, never per source IP. HAProxy limits only the rate of new TLS handshakes per IP, generously.
- Admission control (backfill shed first when Redpanda or ClickHouse lag) works by stretching `next` for batches with the `backfill` flag before live ones (Phase 1).

## 7. Server certificates for devices

Devices and gateways verify ingest's TLS certificate against a **device-ingress CA** that we run, pinned in firmware, rather than the public WebPKI. Public root programmes change on timescales shorter than a device's life, and a device that cannot validate the server cannot be updated. The portal and API use public certificates on their own hostname. (Decision for Phase 1; recorded in CONTRADICTIONS.md C27.)

## 8. Revocation and the device directory

- Every device and gateway has a **credential version** (`cred_version`) and a **status** in PostgreSQL. Claiming, unclaiming, moving, revoking or rotating bumps the version.
- Core maintains the **directory**: for every device and gateway, its serial, status, credential version, keys, product and tier. Ingest holds the whole directory in memory (100k devices with keys is a few MB) and never queries PostgreSQL.
- Core writes each change to a Redis Stream (`dir:changes`) with a monotonically increasing directory version, and a full compressed snapshot (`dir:snapshot`) every 10 minutes and on demand.
- Ingest loads the snapshot at start, then tails the stream. Every 15 s it also reads `dir:version`. A pod whose applied version is behind for more than 60 s, or that cannot reach Redis for 60 s, **fails closed**: it returns 503 and fails its health check so HAProxy drains it.
- So a revocation reaches every ingest pod within seconds normally, and within 60 s in the worst case, or the pod stops taking traffic. Short JWT lifetimes are a further backstop on the direct path.

> **Design note.** The design doc uses Redis pub/sub plus polling. A Redis Stream is not lost on reconnect the way pub/sub is, so the version check becomes a safety net rather than the main mechanism.

## 9. Signing keys

| Key | Where | Signs | Rotation | Verified by |
|---|---|---|---|---|
| Root CA (P-384) | HSM, offline | Intermediates only | 10 years | HAProxy (gateway chain), core |
| Token key (P-256) | Online, core | Device JWTs | 30 days; published in JWKS 24 h before use, kept until its last token expires | Ingest |
| Gateway CA (P-256) | Online, core | Gateway certificates | 1 year | HAProxy |
| Reply key (P-256) | Online, ingest | Per-device replies | Yearly, via `key_rollover` (spec 02 §5.2) | Devices (pinned, no chain) |
| Factory CA (P-256) | Online, core | Jig certificates | 1 year | Core |
| Device-ingress CA (P-256) | HSM | Ingest TLS server certificates | 10 years (pinned in firmware) | Devices, gateways |
| Firmware key (P-256) | HSM, with offline escrow | Firmware images; also the secure-boot / MCUboot key | Effectively never: it is in device bootloaders | Devices, gateways |

- The HSM signs only intermediates and firmware, so it never becomes a bottleneck when a fleet re-authenticates after an outage.
- Losing the firmware key is the one failure with no recovery. It has an offline escrow copy.
- In the development stack every key is a file generated by `scripts/dev-keys.sh`. Development keys are never accepted outside development: production firmware pins production CAs.

## 10. Threats

| Threat | Mitigation |
|---|---|
| Captured Thread traffic replayed | Signatures cover sequence numbers; de-duplication makes replays inert |
| Stolen direct-device token | 1–2 h lifetime; replays de-duplicated; RFC 8705 certificate binding for high-value products (Later) |
| Key extracted from one device | Affects only that device: revoke it. No shared secrets. |
| Forged registration | Jig mTLS bound to one batch manifest |
| Guessing claim codes | 80 bits, rate-limited, only valid while unclaimed |
| Stale revocation state in a pod | Fail closed after 60 s (§8) |
| Algorithm confusion | Algorithm pinned per key; `none` and HMAC never accepted |
| Compromised gateway | Spec 02 §5 |
