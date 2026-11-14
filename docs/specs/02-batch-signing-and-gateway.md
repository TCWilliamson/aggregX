# 02. Batch signing and gateway relay

Status: Draft 1. Depends on spec 01 (envelope, reply) and spec 03 (keys, certificates).

Thread devices reach the cloud through a border router (the **gateway**) that we build and run. This spec defines how devices sign their batches so that the gateway cannot forge or alter their data, how the cloud signs its replies so that the gateway cannot forge acknowledgements or commands, the HTTPS protocol between gateway and ingest, the CoAP protocol between device and gateway, and what a gateway can still do wrong.

## 1. Summary

```
 device ──CoAP/Thread──▶ gateway ──HTTPS + mTLS──▶ HAProxy ──▶ ingest ──▶ Redpanda
   COSE_Sign1(envelope)     relays bytes unchanged          verifies every device signature
   ◀── COSE_Sign1(reply) ◀── relays bytes unchanged ◀──── signs every per-device reply
```

- The device signs every envelope with its factory key (COSE_Sign1, ES256).
- The gateway authenticates itself with its own X.509 certificate (mutual TLS) and forwards the signed bytes untouched.
- Ingest verifies each device signature against the device's registered public key.
- Ingest signs each per-device reply (acks, mailbox items, time) with a cloud **reply key**. Devices verify it with the reply public keys they hold.

## 2. Algorithms

- v1 uses **ES256 only** (ECDSA P-256 with SHA-256, COSE alg −7) for device batches, cloud replies and gateway certificates. P-256 is supported in hardware or well-tested software on every candidate pilot part (nRF52840 CryptoCell-310, nRF54L, ESP32-H2 ECDSA peripheral, any Linux gateway).
- EdDSA (alg −8) is reserved for Later. A device's registered key records its algorithm; ingest **pins** the algorithm from the registry and rejects a batch whose protected header names any other, including `none` or HMAC.
- Signatures use the COSE fixed-length `r ‖ s` form (64 bytes), not DER.
- ECDSA signatures are malleable (a third party can turn a valid signature into another valid one). This is harmless here because nothing is keyed on signature bytes; de-duplication is by `(dev, inc, series, seq)`.

## 3. Device batch: COSE_Sign1 profile

```
18([                                 / COSE_Sign1 tag /
  h'A2012604 44 <kid:4>',            / protected: {1: -7, 4: h'<kid>'} /
  {},                                / unprotected: empty /
  h'<envelope bytes>',               / payload: the spec-01 envelope, attached /
  h'<64-byte signature>'
])
```

| Element | Rule |
|---|---|
| Tag | Tag 18 MUST be present. |
| Protected header | Exactly `{1: -7, 4: kid}`, deterministically encoded (9 bytes + 1 bstr header). Any other label → reject. |
| `kid` | First 4 bytes of SHA-256 over the device's 65-byte uncompressed public key. Selects among a device's keys (rotation); it is not a global identifier. |
| Unprotected header | MUST be an empty map. |
| Payload | MUST be attached (not nil). It is the complete envelope, so the signature covers `dev`, `inc`, every sequence number, every timestamp and every value. |
| External AAD | ASCII `iotp-batch-v1` (13 bytes). Domain separation: a device signature over a batch can never be passed off as a signature over anything else. |
| Overhead | 81–82 bytes over the envelope (verified by `internal/wire` tests). |

### 3.1 Verification at ingest

In this order, stopping at the first failure:

1. Strict CBOR decode: tag 18, 4-element array, protected bstr decodes to exactly `{1: -7, 4: bstr(4)}`, unprotected empty, payload bstr, signature bstr(64).
2. Parse the envelope header from the payload (spec 01 §3.1). `dev` must be a registered device.
3. Look up the device's keys with this `kid` whose status is `active` or `retiring` (spec 03 §2.4) and whose algorithm is ES256.
4. Build `Sig_structure = ["Signature1", protected, h'iotp-batch-v1', payload]` and verify.
5. Only now trust anything in the payload. Continue with the state checks in spec 03 §6.

The header was parsed before verification only to find the key. Nothing parsed from an unverified payload may be logged as fact, counted, or stored.

### 3.2 Who signs what

| Path | Batch | Reply |
|---|---|---|
| Thread device via gateway | COSE_Sign1 by device key: **required** | COSE_Sign1 by cloud reply key: **required** |
| Direct HTTPS device | Unsigned envelope with a bearer JWT (spec 03 §4). A signed batch MAY be sent instead of a JWT (Later). | Unsigned (TLS authenticates the server) |
| Gateway's own telemetry | Unsigned envelope inside the mTLS-authenticated relay request (§4.2) | Unsigned, inside the relay reply |

### 3.3 Signing cost on the device

One ECDSA P-256 signature per batch. On an nRF52840 with CryptoCell-310 this is in the order of tens of milliseconds; batches are sent every 30–300 s, so the energy cost is small next to the radio. This is an estimate to confirm on the pilot part in Phase 1.

### 3.4 Nonce

Every signed batch carries `n` (spec 01 key 17), a fresh random 32-bit value. The cloud echoes it in the reply. It binds a reply to a request for the purpose of time sync (§5.4). It is not used for replay protection of batches; sequence numbers do that.

### 3.5 Cloud reply: COSE_Sign1 profile

Identical to §3 except: `kid` is the first 4 bytes of SHA-256 of the cloud reply key's public key; the payload is the spec-01 reply map, which MUST include `dev`, `inc` and (if the request had one) `n`; external AAD is ASCII `iotp-reply-v1`.

A device MUST verify the reply signature before acting on anything in it, and MUST check that `dev` is its own serial and `inc` its own incarnation.

## 4. Gateway ⇄ ingest (HTTPS)

`POST https://ingest.<cell>.<domain>/v1/gw/relay`, HTTP/1.1 keep-alive or HTTP/2, `Content-Type: application/cbor`, mutual TLS with the gateway certificate (spec 03 §5).

HAProxy verifies the client certificate chain, rejects requests without a valid one on this path, and passes the certificate's subject CN (the gateway serial, e.g. `GW-00007`) to ingest in `X-Client-Identity`. HAProxy deletes any incoming `X-Client-Identity` header before setting its own. The cell is enforced by each cell having its own Gateway CA; the URI SAN (spec 03 §5) is checked by ingest when it loads certificate records.

### 4.1 Request

| Key | Name | Type | Req | Meaning |
|---:|---|---|:-:|---|
| 0 | `v` | uint | ✓ | `1` |
| 1 | `gw` | tstr | ✓ | Gateway serial. MUST equal the serial in `X-Client-Identity`. |
| 2 | `items` | [bstr] |  | Device batches: each element is one COSE_Sign1 exactly as the device sent it. Oldest first per device. ≤ 500. |
| 3 | `poll` | [[dev, inc]] |  | Devices that polled the gateway and need a fresh reply (acks and mailbox) even though they sent nothing new. ≤ 500. |
| 4 | `own` | bytes |  | The gateway's own envelope (spec 01, unsigned; `dev` = gateway serial, schema `gw@1`): its diagnostics, queue depth, link quality, and its own `mres`. |
| 5 | `seen` | [[dev, rssi, lqi, last_ms]] |  | Devices currently attached, for the portal's "devices behind this gateway". |

Request body ≤ 1 MiB.

### 4.2 Reply

| Key | Name | Type | Meaning |
|---:|---|---|---|
| 0 | `now` | uint | Server time, Unix ms |
| 1 | `res` | [result] | One per element of `items`, same order |
| 2 | `polls` | [result] | One per element of `poll`, same order |
| 3 | `own` | reply | Unsigned spec-01 reply for the gateway's own envelope, carrying the **gateway's** acks and mailbox items (gateway OTA, gateway config) |
| 4 | `next` | uint | Seconds until the gateway should send its next relay request if it has nothing queued |

`result = {0: dev, 1: code, 2: reply}`: `dev` is the serial if the batch could be parsed; `reply` is the signed per-device reply (§3.5) when the code is 0 or 1, and for polls.

| Code | Name | Gateway action |
|---:|---|---|
| 0 | accepted | Deliver `reply` to the device. Delete the batch from the gateway queue. |
| 1 | duplicate | All samples were already accepted. As 0. |
| 2 | bad_signature | Delete. Count in diagnostics. Never retry. |
| 3 | unknown_device | Keep up to the gateway's age limit (default 7 days), retry hourly. Device may not be registered yet. |
| 4 | unclaimed | As 3. |
| 5 | revoked | As 3 (a revocation can be a mistake). |
| 6 | throttled | Keep. Retry after the reply's `next` for that device (in `reply` if present) or 60 s. |
| 7 | retry | Transient platform problem. Keep, retry with backoff. |
| 8 | malformed | Delete. Count. Never retry. |
| 9 | too_large | Delete. Count. Never retry. Device firmware bug. |

### 4.3 Request-level status

| HTTP | Meaning | Gateway action |
|---|---|---|
| 200 | Per-item results in the body | As per item |
| 400 | Request not decodable, or `gw` ≠ certificate | Bug: log, retry once, then alarm |
| 403 | Gateway certificate revoked or gateway unclaimed | Keep everything, retry hourly |
| 413 | Request too large | Split |
| 429 | Gateway-level allowance exceeded | Keep, wait `Retry-After` |
| 503 | Platform cannot accept writes | Keep, wait `Retry-After` with jitter |

### 4.4 Semantics

- Ingest processes `items` in order and handles one device's batches sequentially within a request.
- A whole request may be retried after a timeout. Every item is then a duplicate or a re-accept and returns a valid signed reply: relay requests are idempotent.
- A gateway SHOULD keep one relay request in flight, at most two.
- Allowances for Thread devices apply per device identity (spec 03 §6), never per gateway or IP. The gateway also has its own allowance for its own traffic and for the request rate.

## 5. Trust model

### 5.1 What a gateway can and cannot do

| A gateway (or someone who controls one) can | It cannot |
|---|---|
| Delay, drop or reorder device batches. Shows up as sequence gaps, latency and `unexplained loss`. | Create or alter a batch: every batch is signed by the device. |
| Withhold replies, so devices keep data longer and may overflow their outbox. Shows up as outbox depth and `dropped` events. | Forge an ack: acks are inside the signed reply. A device only deletes on a verified cloud ack. |
| Replay old device batches. Harmless: de-duplicated by sequence number. | Forge or alter a mailbox item (config, command, OTA offer, move): items are inside the signed reply. |
| Replay old cloud replies to a device. Harmless: acks are monotonic, items have monotonic ids and expiry (§5.3). | Install firmware: images are signed with the firmware key and verified by the device. |
| Read device payloads. Thread link encryption ends at the border router, so the gateway sees envelopes in clear. | Move a device between orgs or claim it. |
| Feed devices a wrong wall-clock time through its unsigned CoAP time (§5.4). | Make the cloud attribute data to the wrong device. |

> **Design note (C6).** The design doc has the cloud sign acks but says nothing about mailbox items, which reach Thread devices through the same gateway. Unsigned items would let a gateway push config, commands or a `move_boundary` to any device behind it. This spec signs the entire per-device reply.

> **Design note (C17).** The design doc says the gateway gives devices the time "in every CoAP response". That time is not signed, so a compromised gateway can skew timestamps, which is a form of altering data. §5.4 limits the exposure; it is listed as a residual risk.

### 5.2 Cloud reply keys on the device

- Each device holds **up to two** cloud reply public keys (current and next), written by the factory jig (spec 03 §2).
- The cloud signs replies with the current key. To rotate, it sends a `key_rollover` item (spec 01 §7.2) signed by the current key; the device stores the new key in the free slot, then the cloud switches after the new key's `not_before` and once the fleet has had time to receive it.
- The reply key is an online key (spec 03 §7). Its compromise would let an attacker with a path to devices (a gateway) forge replies; rotation is the remedy.

### 5.3 Why replayed replies are harmless

- **Acks**: monotonic. An old reply acknowledges less than the device already knows; it changes nothing.
- **Mailbox items**: the device rejects an item whose `id` is not above its persisted `last_id`, and any item past its `exp`.
- **`now`**: see §5.4.
- **`next`**: an old `next` could make a device wait longer than intended once. Devices cap `next` at the product's maximum reporting interval.

### 5.4 Time on the gateway path

A Thread device has two time sources: the signed `now` in a cloud reply, and the gateway's unsigned time in every CoAP response.

- A device takes the signed `now` only when the reply's `n` matches a batch it sent less than 60 s ago (by its own uptime), and corrects by half the round trip. This defeats replayed replies.
- If no fresh signed reply has arrived (the gateway is offline and replying from its queue), the device MAY use the gateway's CoAP time to set an unset clock.
- When a device sets its clock it sends a `time_sync` event recording the source (spec 01 §4.5), so data stamped from gateway time can be identified later.
- Workers compare synced timestamps with receive time for batches that arrived within a few minutes of capture through an online gateway, and flag devices and gateways whose skew exceeds a threshold (default 30 s).

Residual risk: a compromised gateway that is also offline can make devices behind it stamp readings with a wrong time, undetected until the gateway comes back online. Accepted for the pilot because the gateways are ours.

## 6. Device ⇄ gateway (CoAP)

Pilot. The gateway firmware is built in Phase 1; this section fixes the contract the device SDK and gateway share.

- CoAP (RFC 7252) over UDP over Thread. The gateway advertises `_iotp._udp` via SRP/DNS-SD; devices send to the advertised address.
- **Upload**: confirmable `POST /b`, Content-Format 18 (`application/cose; cose-type="cose-sign1"`), payload = the signed batch. Batches larger than one 802.15.4-friendly payload use block-wise transfer (RFC 7959 Block1).
- **Poll** (nothing to send, wants acks or items): `GET /p?d=<serial>&i=<inc>`.
- **Response** to both: `2.04 Changed` / `2.05 Content`, Content-Format 60 (`application/cbor`), payload `{0: gateway_time_ms, 1: [signed reply…]}`, the gateway's queued signed replies for this device, oldest first, at most 2 (the device asks again if `more` is set).
- **Piggybacked fresh reply**: when the gateway is online it SHOULD forward the batch at once and wait up to 2 s for the cloud's reply so that it can return it in the same exchange. Otherwise it answers immediately with what it holds.
- **Firmware download**: `GET /fw/<artifact_id>` with Block2. The gateway serves cached images (§7).
- **Errors**: `4.00` malformed, `4.13` too large (device splits), `5.03` gateway storage full (device keeps data and backs off).

## 7. Gateway behaviour

- **Store and forward.** A queue per device, forwarded oldest first. The device outbox is the source of truth (a device deletes only on a cloud ack), so evicting a batch from a full gateway queue is not data loss while the device still holds it. Evictions are counted in the gateway's diagnostics.
- **Reply cache.** Holds the latest signed replies per device until delivered, and the device's pending mailbox items, so sleepy devices get them at their next poll.
- **Polls upstream.** When a device polls and the gateway holds no fresh reply, the gateway includes `[dev, inc]` in `poll` on its next relay request.
- **OTA proxy** (Phase 1): fetches an artifact once over HTTPS using a signed URL from its own mailbox, verifies SHA-256, caches, serves by block-wise CoAP, and paces concurrent device downloads. Devices verify the firmware signature themselves.
- **Health.** Reports queue depth, oldest queued age, evictions, signature-failure count, attached devices and link quality in its own envelope.

## 8. Open points

- Payload confidentiality from the gateway (COSE_Encrypt0 inside the signature) is Later. Needed only if gateways are ever run by third parties.
- Whether direct devices should also sign batches instead of using JWTs (simpler firmware, one code path). See CONTRADICTIONS.md C10.
