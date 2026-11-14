# 07. Contract and fault test plan

Status: Draft 1. This is the plan the Phase 0 simulator and test suites implement. It is also how agent-written code gets verified: a change to ingest, workers or storage is not done until these pass.

## 1. Layers

| Layer | What it tests | Runs against | When |
|---|---|---|---|
| L1 Golden vectors | Wire encoding and decoding, byte for byte | Go codec, C codec (zcbor), CDDL validator | Every commit |
| L2 Ingest contract | HTTP behaviour of ingest for every input class | Ingest + Redis + Redpanda (Compose) | Every commit |
| L3 Pipeline faults | End-to-end exactly-once under injected faults | Full Compose stack + simulator | Every merge to main; nightly at scale |
| L4 Tenancy and access | Routing, moves, visibility, leakage | Core + workers + PostgreSQL + ClickHouse | Every merge |
| L5 Scale | 10k devices with faults; Phase 3 adds 50k and reconnect storms | Full stack on the 32-core PC | Phase 0 exit; before each release |

Every L3–L5 test ends with the reconciliation check (spec 06 §11). A test that passes without reconciling has not passed.

## 2. L1: golden vectors

`spec/vectors/` holds `NAME.cbor` (bytes) with either `NAME.diag` (the expected value in CBOR diagnostic notation) or `NAME.error` (the expected error code). Each implementation has a runner: valid vectors must decode to the expected value, and the Go reference encoder must re-encode them byte for byte; invalid vectors must be rejected with the expected code. Vectors are regenerated only deliberately (`go test ./internal/wire -update`) and reviewed as a contract change.

Required vectors (minimum):

| Group | Vectors |
|---|---|
| Envelope valid | every worked example in spec 01 §11; every value type (int, negative int, half/single/double float, bool, string, geo 2 and 4); empty record; max-size sections; all platform event types |
| Envelope invalid | missing each required key; unknown header key; `v = 2`; `dev` lowercase / 33 chars; `inc = 0`; duplicate map key; indefinite-length nested 9 deep; section with negative seq; `null` value; slot 0 and 240 in `mod`; block with `hz = 0` and 4,097 values; trailing bytes after the item |
| Reply | each example; every mailbox type; 16 ack ranges; signed reply with each external AAD |
| COSE | valid ES256 batch (fixed test key); wrong AAD; alg −8, alg 0 (none), alg 5 (HMAC); extra protected label; non-empty unprotected header; detached payload; DER signature; 63-byte signature; untagged; tag 98 |
| Relay | request and reply with all result codes |
| Config hash | canonical config → expected 8-byte hash, for the example schemas |
| Sig structure | the exact `Sig_structure` bytes for the COSE vectors, so C and Go can be compared before hashing |

Test keys in `spec/vectors/keys/` are for vectors only and are rejected by any non-test build.

## 3. L2: ingest contract tests

Go tests that run the ingest binary against Compose services and assert status, reply contents and side effects (Redpanda record present or absent, Redis state).

| # | Case | Expect |
|---|---|---|
| 2.1 | Valid direct batch | 200, `st 0`, ack covers all seqs, exactly one Redpanda record |
| 2.2 | Same bytes again | 200, same acks, **no** new Redpanda record |
| 2.3 | Same samples re-batched differently | 200, acks, no new record |
| 2.4 | Overlapping batch (half new) | One record (whole batch), acks cover the union |
| 2.5 | Batch above a hole | Ack `upto` below the hole, range for the batch, `resend` names the hole |
| 2.6 | Fill the hole | `upto` advances past both |
| 2.7 | Redis flushed between 2.1 and 2.2 | 200, acks, **a new record is published** (I3) |
| 2.8 | Redpanda stopped | 503 with `Retry-After`; no ack; Redis unchanged |
| 2.9 | Redpanda stopped after produce, before ack returns (timeout) | 503; no ack; retry later is accepted |
| 2.10 | Redis stopped | 503 |
| 2.11 | Direct body is an unsigned envelope; unknown `kid`; bad signature; wrong AAD | 401 |
| 2.12 | Signature by a `revoked` key; by a `retiring` key | 401; 200 |
| 2.13 | Envelope `dev` ≠ the signing key's device | 400 |
| 2.14 | Revoked device; unclaimed device | 403, `st 3` / `st 2`, no record |
| 2.15 | Allowance exceeded | 429, `st 1`, `next` > 0, no record |
| 2.16 | 65 KiB body | 413 |
| 2.17 | Unknown schema, unknown channel, out-of-range value, timestamp 60 days old | 200 and acked (never rejected, spec 01 §9) |
| 2.18 | Gateway request with valid, bad-signature, unknown-device, malformed items mixed | 200; per-item codes 0, 2, 3, 8 in order; one record per valid item |
| 2.19 | Gateway with no client identity; `gw` ≠ certificate | 401 / 400 |
| 2.20 | Gateway poll for a device with queued mailbox items | Signed reply with items in id order, ≤ 8, `more` set if more |
| 2.21 | Each signed reply | Verifies with the reply key and AAD `iotp-reply-v1`; contains `dev`, `inc`, `n` |
| 2.22 | Directory snapshot stale beyond 60 s | Ingest returns 503 and fails its health check |
| 2.23 | Revocation published | Takes effect on the next request (and within 60 s worst case) |

## 4. L3: pipeline fault scenarios

The simulator (§6) drives these against the full stack. Each scenario runs with a few hundred devices for L3, and is included in the 10k mix for L5.

### 4.1 Duplicates

| Case | Injection | Must hold |
|---|---|---|
| D1 | Every batch sent twice | Each sample once in `FINAL` |
| D2 | Reply dropped after ingest acked: device resends | Once |
| D3 | Resend re-batched with different boundaries and `t0` | Once, same `ts` |
| D4 | Concurrent identical requests to two ingest instances | Once |
| D5 | Worker killed after ClickHouse insert, before offset commit | Once after replay; rollups correct |
| D6 | Worker killed after PostgreSQL commit, before offset commit | Once; gap state and metering not double-counted |
| D7 | Redis flushed mid-run | Once (published again, merged by storage) |
| D8 | Gateway replays a day of old batches | Once |

### 4.2 Gaps and loss

| Case | Injection | Must hold |
|---|---|---|
| G1 | Drop 5% of batches in transit, device keeps them | All arrive via resend; zero loss; no `unexplained` gaps |
| G2 | Device outbox overflow, `dropped` events sent | Gaps `dropped`; acks advance past them; reconciliation counts them as declared |
| G3 | Device destroys samples silently (simulated flash fault) | Gaps become `unexplained` after the timeout, and only those |
| G4 | Reboot with `boot_skip` | Gap `skipped`; acks advance |
| G5 | Gateway silently drops one device's traffic for 2 hours, device keeps data | Pending gaps, then filled when traffic resumes; zero loss |

### 4.3 Reordering and backlogs

| Case | Injection | Must hold |
|---|---|---|
| R1 | Batches delivered in random order within a 10-minute window | Once each; ordering by seq in queries |
| R2 | Device offline 30 days at 5 s sampling, then drains oldest first | All stored; `backfill` flag set; rollups recomputed; alert rules ignore it |
| R3 | Device offline 31 days (beyond the 30-day late window) | Over-age samples quarantined, not lost, and acked |
| R4 | 2,000 devices return from a 6-hour outage at once | Live data accepted before backfill; `next` stretches; no 5xx beyond admission control; reconciliation passes |
| R5 | Gateway offline 12 hours with 50 devices behind it | Store-and-forward; all stored once |
| R6 | Series interleaved: module records arrive before main records of the same period | Once each |

### 4.4 Clocks

| Case | Injection | Must hold |
|---|---|---|
| T1 | Device with no clock for first 10 minutes of each boot (uptime batches), resends after syncing | Uptime samples keep uptime `ts_dev`; all of one boot share one anchor; resends merge |
| T2 | Device clock 3 hours fast | Stored as sent, `clock_future` flagged; anchoring not applied |
| T3 | Clock jumps backwards mid-batch | Ordering by seq preserved; `clock_jump` event stored |
| T4 | Uptime backlog spanning three reboots | Three anchors; no cross-boot mixing |
| T5 | Gateway feeds a wrong time while offline | Samples stored as sent; device and gateway flagged when skew is measurable |
| T6 | Batch with a `time_sync` event as first contact of a boot | Anchor taken from the event, exactly |

### 4.5 Signatures and forgery

| Case | Injection | Must hold |
|---|---|---|
| S1 | Bit flipped in payload | Code 2; nothing stored; gateway deletes it |
| S2 | Batch signed by device A's key with `dev = B` | Code 2 |
| S3 | Gateway alters a seq, a value, or `inc` | Code 2 |
| S4 | Gateway injects a batch for a device it never saw | Code 2 |
| S5 | Unknown `kid`; revoked key; `retiring` key | 2; 2; accepted |
| S6 | alg confusion vectors from §2 | Rejected |
| S7 | Gateway forges a reply (acks, a `cfg` item) | Device rejects (simulated device verifies) |
| S8 | Gateway replays an old signed reply carrying a `cfg` item | Device rejects the item (`id ≤ last_id`); acks unchanged |
| S9 | Reply for another device or another `inc` | Device ignores it |

### 4.6 Composition, schemas and incarnations

| Case | Injection | Must hold |
|---|---|---|
| M1 | Module swapped in slot 1 during a 2-day backlog | Readings before the swap on the old module's sensor instance, after on the new |
| M2 | Unknown module serial | Stored; sensor instance created; alert |
| M3 | Firmware sends a schema version not yet published | Quarantined; after publishing, replay stores it once |
| M4 | Factory reset: new `inc`, counters from 0, old outbox lost | New samples stored, none discarded as duplicates of old ones |
| M5 | Factory reset with old-incarnation samples still in flight | Both incarnations stored; old ones route by their incarnation |

### 4.7 Platform failures

| Case | Injection | Must hold |
|---|---|---|
| P1 | ClickHouse down 30 minutes | Ingest keeps acking; workers stall; catch up after; reconciliation passes |
| P2 | PostgreSQL down 2 minutes | Workers stall and retry; no loss |
| P3 | Ingest restarted every 30 s during load | Devices see 503s or timeouts and resend; reconciliation passes |
| P4 | Redpanda restarted | 503s while down; nothing acked is lost |
| P5 | Network partition between ingest and Redis | 503; no ack without the round trip |
| P6 | 1,000 simulated devices return after 7 days offline, each with a full backlog, while 10,000 others send live data (spec 01 §7.5) | Every backlog sample stored exactly once; live-data p99 ingest latency within its normal target throughout; backfill batches paced with `next`, none refused; reconciliation passes |

## 5. L4: tenancy and access

| Case | Must hold |
|---|---|
| A1 | Isolated move with a backlog spanning the boundary: samples below the boundary in org A's stream, at or above in org B's, whatever order they arrive in |
| A2 | Isolated move pending while the device is offline for 3 days | All samples stay in A until the device applies it |
| A3 | Move cancelled before apply; after apply | No-op; applied anyway |
| A4 | With-data move | Same stream id; org B sees all history; org A sees none |
| A5 | Unclaim, then re-claim by another org | Data between the two goes to the new owner; nothing of the old owner's is visible |
| A6 | Move then replay the whole Redpanda window | Identical stream ids on every row (I5) |
| A7 | For every API endpoint: a user of org A requests B's device, stream, group, export | 404 (not 403), and no rows |
| A8 | User lacking a data class | Those channels absent from every endpoint and export |
| A9 | Group view across a membership change | Each device contributes only its member periods |

## 6. The simulator

Go, `cmd/simulator`. One process simulates thousands of devices and gateways.

- **Device model**: the same state machine the SDK implements: per-series counters with persisted blocks, `inc`, boot counter, outbox with capacity, batching, signing (ES256, real keys from a test batch), acks, resend handling, jittered backoff, `next`, mailbox processing with `last_id`, `move_boundary`, time sync rules, module swaps. It is the executable reference for the SDK.
- **Gateway model**: store and forward, reply cache, polls, relay protocol, mTLS with a test certificate.
- **Products**: driven by the schemas in `spec/schemas/`, with configurable sampling, reporting, burst triggers and module populations.
- **Fault injector**: per scenario: drop, duplicate, delay, reorder, corrupt, partition (per device, per gateway, per path), clock offsets and drifts, reboots, factory resets, outbox overflows, silent flash faults. Network-level faults between simulator and HAProxy use a Toxiproxy container; device-level faults are in process.
- **Journal**: every sample generated (device, inc, series, seq, channel, value, true time, timebase, boot) and every declared drop and silent destruction, written to a ClickHouse table `sim.journal` for reconciliation by SQL.
- **Clock**: real time for L3–L5; a virtual-time mode for long backlogs (R2) that compresses days into minutes by generating historical samples directly into outboxes.
- **Determinism**: every run has a seed; a failing run can be replayed exactly.

## 7. Phase 0 exit test

From the design doc: *10,000 simulated devices through ingest, Redpanda and workers into ClickHouse on the PC, with injected faults, and the stored data reconciles exactly with what the simulator sent.* Concretely:

- 10,000 devices, 60% behind 100 simulated gateways and 40% direct, on the reference mix (design doc "Scaling": 70% every 15 min, 25% every minute, 5% at 1 Hz, 2% in bursts).
- Two hours of real time plus a 30-day virtual backlog for 500 devices.
- Faults active throughout: D1–D7, G1–G4, R1, R4 (scaled to 2,000), T1–T4, S1–S4, M1, M4, P1 and P3 once each.
- Pass: reconciliation (spec 06 §11) with zero discrepancies; no unexplained gap other than those G3 created; ingest p99 latency under 200 ms outside injected outages; worker lag back under 10 s within 5 minutes of the last fault.
