# 03. Identity, claiming and revocation

Status: Draft 1. Depends on spec 01 and 02.

**Principle:** identity comes from the device, tenancy from the server. Every device and gateway has its own key, generated at manufacture. No secret is shared between devices. Which org a device belongs to is a fact in the platform's database, never something the device asserts.

The "company or project token" idea from the original brief survives in two places: as a **claim code** used once at installation (§3), and as order tokens for pre-assignment (§3.4). Devices carry no tenancy or routing credential (§4).

## 1. Identities

| Identity | Credential | Proves itself by | Lifetime |
|---|---|---|---|
| Device (Thread) | P-256 factory key pair, private key on the device | Signing every batch (spec 02 §3) | Life of the device; rotatable Later |
| Device (direct) | Same factory key | Signing every batch (§4, spec 02 §3) | Life of the device; rotatable Later |
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

## 4. Direct devices (no tokens)

Direct (WiFi) devices authenticate exactly as Thread devices do: **every batch is a COSE_Sign1 signed by the device's factory key** (spec 02 §3), posted to `POST /v1/ingest`. Every reply is a COSE_Sign1 signed by the cloud reply key (spec 02 §3.5). There are no device tokens, no token service and no token keys. (Decided 14 Nov, CONTRADICTIONS.md C10.)

- **Freshness.** Replayed batches are inert: the signature covers the sequence numbers, so a replay is a duplicate (spec 06 §3). The nonce `n` binds a reply to its request (spec 02 §3.4).
- **Polling.** A direct device with no samples that wants its mailbox sends a signed batch holding one heartbeat record (a record with an empty value map, spec 01 §4.1). There is no separate poll message.
- **Routing.** Nothing about routing comes from the device's credential. The cell is the ingest hostname written at manufacture (§2.2); the Redpanda partition is the device serial (spec 06 §2); tenant and stream are resolved by workers from ownership boundaries (spec 05 §3.3).
- **Cost.** One ES256 verification per batch at ingest, in the order of 50–100 µs of CPU; 100k devices uploading every 60 s is about 1,700 verifications a second, a fraction of one core.

> **Design note (C9, C10).** The design doc gives direct devices short-lived JWTs whose sample includes a `stream` claim. Routing must depend on sequence numbers and ownership boundaries, never on a credential issued before a move (spec 05 §5), so the claim was removed first (C9). With it gone the token carried nothing a signature does not, while adding a second identity mechanism to firmware and ingest, a token service on the path of every WiFi upload, and a need for the device to know the time before its first upload. A signature also proves who produced the data, and that proof stays with the batch through the outbox and the archive; a token proves only who held the connection.

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
| 3 | **Identity.** Gateway: `X-Client-Identity` present, gateway active in the directory, `gw` matches. Then, on both paths, per batch: COSE verification (spec 02 §3.1) | Direct 401; gateway 401 / 403, per-item code 2 |
| 4 | Envelope header (spec 01 §6). `dev` equals the signing key's device | 400 / code 8 |
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
- **Backfill volume**: batches flagged `backfill` count against a separate, larger allowance sized for the product's longest expected offline period. Over-allowance backfill gets a longer `next`; it is never answered with `st = 1`, so a returning device is slowed, not stranded (spec 01 §7.5).
- Values come from the device's tier (product plan × power profile). Gateways have their own bucket for their own traffic and request rate. Per-org buckets sit on top (Phase 3).
- An over-allowance batch is not stored and not acknowledged; the reply has `st = 1` and a `next` long enough to bring the device back under its rate. The device keeps the data.
- Limits are per device or gateway identity, never per source IP. HAProxy limits only the rate of new TLS handshakes per IP, generously.
- Admission control (backfill shed first when Redpanda or ClickHouse lag) works by stretching `next` for batches with the `backfill` flag before live ones (Phase 1).

## 7. Server certificates for devices

Devices and gateways verify ingest's TLS certificate against a **device-ingress CA** that we run, pinned in firmware, rather than the public WebPKI. Public root programmes change on timescales shorter than a device's life, and a device that cannot validate the server cannot be updated. The same private PKI issues gateway client certificates (§5).

The portal, the API and anything a browser opens use certificates from a public CA (Let's Encrypt, or Cloudflare edge certificates if Cloudflare fronts the portal) on their own hostnames. Device ingress hostnames are never behind a TLS-terminating proxy we do not run, because gateway mTLS must reach HAProxy (spec 02 §4). (Decided 14 Nov, CONTRADICTIONS.md C27; key ceremony and HSM choice are Phase 1.)

## 8. Revocation and the device directory

- Every device and gateway has a **credential version** (`cred_version`) and a **status** in PostgreSQL. Claiming, unclaiming, moving, revoking or rotating bumps the version.
- Core maintains the **directory**: for every device and gateway, its serial, status, credential version, keys, product and tier. Ingest holds the whole directory in memory (100k devices with keys is a few MB) and never queries PostgreSQL.
- Core writes each change to a Redis Stream (`dir:changes`) with a monotonically increasing directory version, and a full compressed snapshot (`dir:snapshot`) every 10 minutes and on demand.
- Ingest loads the snapshot at start, then tails the stream. Every 15 s it also reads `dir:version`. A pod whose applied version is behind for more than 60 s, or that cannot reach Redis for 60 s, **fails closed**: it returns 503 and fails its health check so HAProxy drains it.
- So a revocation reaches every ingest pod within seconds normally, and within 60 s in the worst case, or the pod stops taking traffic.

> **Design note.** The design doc uses Redis pub/sub plus polling. A Redis Stream is not lost on reconnect the way pub/sub is, so the version check becomes a safety net rather than the main mechanism.

## 9. Signing keys

| Key | Where | Signs | Rotation | Verified by |
|---|---|---|---|---|
| Root CA (P-384) | HSM, offline | Intermediates only | 10 years | HAProxy (gateway chain), core |
| Gateway CA (P-256) | Online, core | Gateway certificates | 1 year | HAProxy |
| Reply key (P-256) | Online, ingest | Per-device replies | Yearly, via `key_rollover` (spec 02 §5.2) | Devices (pinned, no chain) |
| Factory CA (P-256) | Online, core | Jig certificates | 1 year | Core |
| Device-ingress CA (P-256) | HSM | Ingest TLS server certificates | 10 years (pinned in firmware) | Devices, gateways |
| Firmware key (P-256) | HSM, with offline escrow | Firmware images; also the secure-boot / MCUboot key | Effectively never: it is in device bootloaders | Devices, gateways |

- The HSM signs only intermediates and firmware, so it never becomes a bottleneck.
- Losing the firmware key is the one failure with no recovery. It has an offline escrow copy.
- In the development stack every key is a file generated by `scripts/dev-keys.sh`. Development keys are never accepted outside development: production firmware pins production CAs.

## 10. Threats

| Threat | Mitigation |
|---|---|
| Captured Thread traffic replayed | Signatures cover sequence numbers; de-duplication makes replays inert |
| Captured direct-device traffic replayed | Same as Thread: signatures cover sequence numbers; replays are duplicates |
| Key extracted from one device | Affects only that device: revoke it. No shared secrets. |
| Forged registration | Jig mTLS bound to one batch manifest |
| Guessing claim codes | 80 bits, rate-limited, only valid while unclaimed |
| Stale revocation state in a pod | Fail closed after 60 s (§8) |
| Algorithm confusion | Algorithm pinned per key; `none` and HMAC never accepted |
| Compromised gateway | Spec 02 §5 |
