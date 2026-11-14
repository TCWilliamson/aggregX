# Design doc issues found while writing the specs

Against *IoT Cloud Platform: Architecture & Scaling Design*, revision 44. Each item names where in the design doc the problem is, what is wrong, and what the specs do about it. **Nothing here has been changed in the design doc.** Items marked **Decision** need tom's call; the specs currently follow the recommendation, and all of them are cheap to change until code depends on them.

Severity: **High** = would lose, duplicate or leak data, or open a security hole, if built as written. **Medium** = ambiguity that two implementers would resolve differently. **Low** = wording or minor inconsistency.

## Summary

| # | Severity | Topic | Status |
|---|---|---|---|
| C1 | High | Uptime timestamps anchored per upload break de-duplication | Fixed in spec (boot anchors) |
| C2 | High | Retention class on the stream, not the row | Fixed in spec (effective-dated, stamped per row) |
| C3 | High | One `ack` for several sequence series | Fixed in spec (per-series acks) |
| C4 | High | `move_boundary` acknowledged with a single number | Fixed in spec (per-series vector) |
| C5 | Medium | Sequence numbering of events and blocks undefined | Fixed in spec (series 0/1/2/16+slot) |
| C6 | High | Mailbox items through the gateway not signed | Fixed in spec (whole reply signed) |
| C7 | Medium | Redpanda keyed by stream id | Fixed in spec (keyed by device) |
| C8 | Low | Archive partitioning stated two ways, one impossible | Fixed in spec (time + device hash) |
| C9 | Medium | JWT carries a `stream` claim | Fixed in spec (removed) |
| C10 | Medium | Direct/JWT path tagged Pilot, but pilot is Thread-only | Closed 14 Nov (option 3: every device signs, no JWTs) |
| C11 | Medium | Ingest rejects disallowed schemas, stranding device data | Fixed in spec (accept and quarantine) |
| C12 | Low | Out-of-range values quarantined | Fixed in spec (stored, flagged) |
| C13 | High | Factory reset restarts counters, new data discarded as duplicates | Fixed in spec (incarnation id) |
| C14 | Medium | De-dup state "per stream" but sequence numbers are per device | Fixed in spec (per device, incarnation, series) |
| C15 | Medium | Session events: one message or two? | Fixed in spec (sent once, complete) |
| C16 | Medium | "Go and C generated from one spec file" | Closed 14 Nov (option 1) |
| C17 | Medium | Gateway time to devices is unsigned | Mitigated; residual risk documented |
| C18 | Medium | Order of worker side effects before offset commit | Fixed in spec |
| C19 | Low | Ingest must build mailbox replies without PostgreSQL | Fixed in spec |
| C20 | Low | Late-data age check placed in ingest | Fixed in spec (workers) |
| C21 | Low | Phase 0 scope vs 3–4 weeks | Risk noted |
| C22 | Low | "Two layers" vs "three layers" of de-duplication | Fixed in spec |
| C23 | Low | Reboot resumes counters from saved block only | Fixed in spec (also scan outbox) |
| C24 | Medium | Data from unclaimed devices undefined | Fixed in spec (not stored, device keeps it) |
| C25 | Medium | Mailbox ids are strings | Fixed in spec (per-device monotonic integers) |
| C26 | Low | Revocation via Redis pub/sub | Improved (Redis Stream + version poll) |
| C27 | Medium | Which CA devices trust for ingest TLS | Closed 14 Nov (own CA for devices, public CA for portal/API) |
| C28 | Medium | Pilot hardware | **Proposed**, awaiting tom's confirmation |
| C29 | High | Samples past the late window are acked, then quarantined: the device deletes data that is never stored | **Decision** (found by customer scenarios X1) |
| C30 | High | Boot anchor for uptime backlogs is wrong and can never be corrected | **Decision** (customer scenarios X2) |
| C31 | Medium | `readings` row volume scales with channel count; block samples as rows | **Decision**, needs benchmarks (customer scenarios X3) |

## Details

### C1. Uptime anchoring breaks de-duplication (High)

**Design doc:** "The envelope" (`ts: uptime` … "the platform anchors uptime timestamps to the receive time"), "Time", "De-duplication: two layers" (storage key includes the timestamp), "Telemetry store design" (ORDER BY includes `ts`, partitioned by day).

**Problem:** If each upload's uptime samples are anchored to that upload's receive time, a resent sample gets a different `ts`. ReplacingMergeTree de-duplicates only rows with identical sorting keys in the same partition, so the resend and the original never merge, and `FINAL` returns both. If the two receive times straddle midnight the copies are in different partitions too. Every resend of an uptime sample would be double-counted.

**Spec:** One anchor per `(device, incarnation, boot)`, fixed the first time any worker sees that boot and never changed (spec 01 §8, spec 06 §4.3, table `boot_anchors`). Generalised as invariant I5: every partition and sorting key column must be stable under replay (spec 06 §4.1).

### C2. Retention class must be on the row (High)

**Design doc:** "Tiers" ("Retention is set per retention class (a column on the stream), not per row").

**Problem:** ClickHouse TTL can only see columns of the row, and dropping whole parts requires the class in the partition key. If a worker reads the stream's *current* class, a sample replayed after a class change lands in a different partition from its first copy and never merges.

**Spec:** Effective-dated `stream_retention`; each row stamped with the class valid at its own timestamp; changes take effect from a future time (spec 06 §4.2).

### C3. Acks per series (High)

**Design doc:** "The reply (one schema)": a single `ack {upto, ranges}` and `resend`, while "The envelope" and "Sequence numbers" give each sub-module its own series.

**Problem:** A device cannot tell which series an ack refers to; a module's samples could be deleted on the strength of the main series' ack.

**Spec:** `ack` and `resend` are maps keyed by series id (spec 01 §7.1).

### C4. Move boundary is a vector (High)

**Design doc:** "Moves" ("acknowledges it with its current sequence number, which becomes the boundary").

**Problem:** With several series there is no single "current sequence number". Using the main series' number alone would mis-route module samples and events.

**Spec:** The device reports the next sequence number for every series it has (spec 01 §7.4), and routing compares per series, with the incarnation ordinal first (spec 05 §3.1). The SDK ordering rule that makes this safe is stated explicitly (spec 05 §6.3).

### C5. Sequence numbers for events and blocks (Medium)

**Design doc:** "Sequence numbers" ("Every sample gets a number … An upload carries the first number; records inside are consecutive"), "The envelope" (`blk`, `ev` without numbering), "Storage shape" (events keyed on seq).

**Spec:** Series 0 main records, 1 events, 2 high-rate blocks, 16+slot modules; each section carries its first number (spec 01 §4).

### C6. Unsigned mailbox items on the gateway path (High)

**Design doc:** "Trust model: the gateway relays, it cannot forge" (only acks are signed), "Mailbox" (gateway keeps a copy of items).

**Problem:** A compromised gateway could send any device behind it config, commands, OTA offers or a `move_boundary`. Firmware signatures stop it installing images, but not the rest.

**Spec:** The cloud signs the whole per-device reply (spec 02 §3.5). Items carry per-device monotonic ids and an expiry so replays are harmless (spec 02 §5.3).

### C7. Redpanda partition key (Medium)

**Design doc:** "100,000 devices" ("Redpanda topics with 48 partitions keyed by stream id"), "Off-the-shelf stateful services".

**Problem:** Ingest does not know the stream of each sample (routing is per sample by sequence number), and an isolated move splits one device's data between streams while its backlog is still arriving. Keying by stream would let the two halves be processed out of order.

**Spec:** Key = device serial (spec 06 §2).

### C8. Archive partitioning (Low)

**Design doc:** "Storage layout implication" (archive partitioned by time, ordered by stream) vs "Raw to cold storage" ("partitioned by stream and date").

**Problem:** The exporter reads raw batches from Redpanda before streams are resolved, so it cannot partition by stream; and per-stream partitioning gives millions of tiny files at 100k devices.

**Spec:** `date/hour/hash(serial) % 16` (spec 06 §10).

### C9. `stream` claim in the device JWT (Medium)

**Design doc:** "Three stages" token example.

**Problem:** Routing must never use the token (the design doc says so itself under "Moves"). A `stream` claim is unused and invites that bug.

**Spec:** Removed (spec 03 §4.2).

### C10. Is the direct (WiFi/JWT) path in the pilot? (Closed 14 Nov: option 3)

**Design doc:** "Device identity" is tagged Pilot and specifies JWTs for direct devices; "Phase 3" lists "Second SDK target … (e.g. ESP32 over WiFi with JWTs)"; the first product is Thread.

**Options:**
1. **Recommended:** specify the direct path now (done), build ingest support and simulator coverage in Phase 0/1 because it is cheap on the server, but no device SDK for it until Phase 3.
2. Defer all of it to Phase 3.
3. Drop JWTs: direct devices sign batches exactly like Thread devices (COSE_Sign1 over HTTPS). One code path in firmware and ingest, no token service, no clock needed for token expiry. Costs ~80 bytes and one signature per upload, irrelevant on WiFi. Worth considering seriously; it removes a whole subsystem.

**14 Nov:** now recommending option 3. A token proves who is connected; a signature proves who produced the data, and that proof stays attached to the batch through the outbox, any relay and the archive. Thread devices must sign anyway (the gateway is not trusted), so tokens would be a second identity mechanism in firmware, ingest and the claim flow. Tokens also need the device to know the time before its first upload and a token-issuing service that becomes a dependency of every WiFi upload. tom, 14 Nov: "fine with all devices signing". He asked whether tokens were wanted for efficient routing: they were not needed for it, because routing never uses the device credential (cell from the factory-set hostname, partition from the serial, stream from ownership boundaries; spec 03 §4).

**Done:** spec 03 §4 now specifies signed direct uploads and the token service, token key and JWT tests are gone; replies to direct devices are signed too, so firmware has one verify path and a device deletes data only on a signature-verified ack on every path (spec 01 §7.1). A direct device polls by sending a heartbeat record. Direct uploads use content type `application/cose; cose-type="cose-sign1"`; the ingest skeleton enforces it.

### C11. Rejecting disallowed schemas at ingest (Medium)

**Design doc:** "Validating on every request", step 4 ("schema allowed for the product") vs "Validation and quarantine" (workers quarantine).

**Problem:** A rejected batch is never acknowledged, so it sits in the device outbox forever, blocks it, and eventually forces a `dropped`.

**Spec:** Ingest checks only identity consistency; anything about content is accept-and-quarantine (spec 01 §9).

### C12. Out-of-range values (Low)

**Design doc:** "Validation and quarantine" ("Anything that fails … goes to quarantine").

**Spec:** Out-of-range values are stored with a flag; only undecodable or unknown content is quarantined (spec 04 §7).

### C13. Factory reset and counters (High)

**Design doc:** not covered.

**Problem:** After a reflash or factory reset the device restarts its counters. New samples reuse old numbers; ingest's Redis state marks them already accepted and acknowledges them without storing them. Silent data loss.

**Spec:** A random incarnation id, regenerated whenever counters cannot be trusted, is part of every sequence identity (spec 01 §5.3), and the incarnation ordinal is part of every storage key.

### C14. De-dup state keyed per stream (Medium)

**Design doc:** "De-duplication: two layers" ("Ingest, per stream, in Redis").

**Problem:** Sequence numbers belong to the device, not the stream, and ingest does not know the stream per sample.

**Spec:** Keyed by device, incarnation and series (spec 06 §3).

### C15. Session events (Medium)

**Design doc:** "Burst (fast-logging) sessions" ("opens a burst session event … and closes it"), events keyed on (stream, type, start, seq).

**Spec:** Events are sent once, complete, with start and duration; optional `session_progress` events for long sessions (spec 01 §4.4).

### C16. Code generation (Closed 14 Nov: option 1)

tom, 14 Nov: fine with the C and Go code being whatever makes sense. Option 1 stands as specified in spec 04 §8.

**Design doc:** Phase 0 ("The envelope and schemas are defined once in a spec file, with Go and C code generated from it").

**Options:**
1. **Recommended:** the wire format's single source is a CDDL file; C is generated with zcbor (Nordic's generator, already in nRF Connect SDK); the Go codec is hand-written and checked against the CDDL and golden vectors in CI. Product schemas are YAML; `iotgen` generates Go and C constants from them (spec 04 §8).
2. Our own YAML format and our own generators for both languages: one source in the literal sense, but we would be writing and maintaining a C CBOR code generator.

### C17. Gateway time (Medium)

**Design doc:** "What the gateway does" ("gives devices the current time … in every CoAP response").

**Problem:** That time is unsigned; a compromised gateway can make devices mis-stamp readings, a form of forging data that the trust model says is impossible.

**Spec:** Devices prefer the signed `now` from a fresh reply, record the time source, and the cloud flags skew (spec 02 §5.4). Residual risk while a compromised gateway is offline is accepted for the pilot.

### C18. Worker side effects (Medium)

**Design doc:** "Chain of custody" ("Workers commit their Redpanda offsets only after ClickHouse confirms").

**Problem:** Workers also write PostgreSQL (gaps, dirty buckets, mailbox results, twin) and Redis. The order and idempotency of those decides whether a crash loses a dirty mark or double-counts metering.

**Spec:** ClickHouse, then one PostgreSQL transaction, then Redis, then commit; each idempotent (spec 06 §4).

### C19. Mailbox replies at ingest (Low)

**Design doc:** "Mailbox" (PostgreSQL with a Redis fast path), "Deployment shape" (ingest talks only to Redis and Redpanda).

**Spec:** Pending items, with their bodies, are mirrored to Redis by core; ingest reads them in its single round trip and signs replies with the reply key it holds (spec 03 §6, §9).

### C20. Late-data age check (Low)

**Design doc:** "Late and offline data" ("Ingest accepts out-of-order data up to a configurable age").

**Problem:** For uptime batches ingest cannot know the age; and rejecting strands data (C11).

**Spec:** Workers quarantine over-age samples; ingest always accepts (spec 01 §9, spec 06 §8).

### C21. Phase 0 scope (Low, risk)

Phase 0 (3–4 weeks) holds the specs, the hardware decisions, the repo and Compose stack, the simulator with full fault injection, and a 10k-device reconciling run. The simulator and reconciliation alone are several weeks of agent work plus review. Suggest treating the hardware decisions as a parallel track and accepting 5–6 weeks.

### C22. Layers of de-duplication (Low)

**Design doc:** "De-duplication: two layers" vs "Telemetry store design" ("three layers").

**Spec:** Redis sequence state (optimisation), insert tokens (optimisation), ReplacingMergeTree + `FINAL` (the guarantee). Same content, one description (spec 06 §1, §4).

### C23. Counter resume after reboot (Low)

**Design doc:** "Sequence numbers" (high-water mark every 1,000 numbers, jump to next block).

**Spec:** Resume at the larger of the next block and the highest number in the outbox + 1 (spec 01 §5.2).

### C24. Unclaimed devices (Medium)

**Design doc:** not covered.

**Spec:** Not stored, not acknowledged; device keeps the data in its outbox; once claimed, outbox contents go to the first owner's stream (spec 03 §3.6, spec 05 §4).

### C25. Mailbox ids (Medium)

**Design doc:** "The reply" (`"id": "mb_19"`).

**Spec:** Per-device strictly increasing integers, persisted on the device as `last_id` (spec 01 §7.2).

### C26. Revocation transport (Low)

**Design doc:** "Revocation that cannot be missed" (change notifications plus snapshot polling).

**Spec:** Changes on a Redis Stream (not lost on reconnect like pub/sub), plus version polling and fail-closed (spec 03 §8).

### C27. Which CA devices trust (Closed 14 Nov)

**Design doc:** not covered.

**Recommendation:** a private device-ingress CA pinned in firmware for device and gateway hostnames; public certificates for the portal and API (spec 03 §7). Public root programmes change faster than a device's service life.

**14 Nov:** tom asked whether something well trusted such as Cloudflare would do. Proposed split:
- **Portal, API and anything a browser opens:** a public CA (Let's Encrypt, or Cloudflare's edge certificates if Cloudflare fronts the portal). Browsers already trust these and renewal is automatic.
- **Device and gateway ingress:** our own private CA, root and intermediates kept offline, with the root's public key pinned in firmware. Devices may run 10+ years; public roots are rotated and distrusted on a 2–5 year cycle, and a device whose trusted root is withdrawn can no longer connect to receive the update that would fix it. A private CA also lets us issue gateway client certificates (spec 02 §2), which public CAs no longer do for this use. Cloudflare must not terminate device TLS, because ingress depends on mTLS reaching HAProxy (spec 02 §4).

tom, 14 Nov: "use whatever CA you recommend". Adopted as above (spec 03 §7). Key ceremony and HSM choice remain Phase 1 work.

### C28. Pilot hardware (Decision, Phase 0)

**Design doc:** "On-device requirements", "Phase 0" (MCU, flash size, MCUboot slot layout, outbox location, border router hardware).

The specs assume ES256 because every candidate part supports it in hardware or well-tested software (spec 03 §2.3). The flash budget decides outbox size, which decides how long a device survives an outage, which feeds the written recovery targets.

**Proposed pilot set (14 Nov), from the test hardware tom has:**

| Role | Hardware | Notes |
|---|---|---|
| Thread device (pilot chip) | Panasonic PAN B511-1C (nRF54L15, to be confirmed) | KMU + TF-M key storage; ~1.5 MB RRAM fits MCUboot dual slots and an outbox without external flash; SDK on nRF Connect / Zephyr |
| WiFi (direct) device, second Thread option | ESP32-C6 | Secure boot + flash encryption; hardware ECDSA peripheral to be checked in the datasheet |
| Gateway | ESP Thread Border Router (ESP32-S3 + ESP32-H2) | The two-chip reference design. The relay runs as C on ESP-IDF on the S3, not as a Go agent on Linux as the design doc assumed; gateway store-and-forward buffer is flash-limited, which is acceptable because devices hold data until the cloud acks it (spec 02 §7) |

**14 Nov, tom:** the ESP32-C6 runs both Thread (802.15.4) and WiFi, so one board can be reflashed for either role; tom is considering a batch of 10–20 for a test fleet. Consequences proposed:
- The device SDK is a portable C core (envelope, outbox, sequence state, signing, reply handling) with a thin platform layer: ESP-IDF first (C6 test fleet, border router), Zephyr/nRF Connect second (PAN B511). The same core runs in both, and in the simulator's host build for unit tests.
- The C6 fleet is the hardware-in-the-loop rig: devices flashed by script over USB, each run recorded, used for soak, outage and fault tests alongside the software simulator.
- **tom, 14 Nov:** ESP32-C6, nRF54 (nRF54L15) and nRF52 (nRF52840) are all realistic production chips for customers, so the SDK supports all three as first-class targets, not one pilot chip. Each target gets a profile (key storage per spec 03 §2.3, outbox size, max batch items and block values as in the c-vectors device profile) and runs the same contract and vector tests in CI. nRF52840 is the tightest (256 KB RAM, 1 MB flash), so its profile sets the floor for buffer sizes.

### C29. Acknowledged, then quarantined (High, Decision)

**Found by:** customer scenarios X1 (`/mnt/project-files/iot-platform/specs/customer-scenarios.md`, scenarios 1, 6, 11, 12). **Not in the design doc; a defect in these specs.**

**Problem:** spec 06 §8 quarantines samples older than the late-data window (default 30 days). Ingest has already acked them (spec 01 §9 forbids rejecting on age, correctly), so the device deletes them (I1). They then exist only in `quarantine`, outside every query. Spec 06 §1 promises an acked sample is in ClickHouse. A pharma shipper with a 45-day trip, or a bridge logger with a 6-month SD backlog, silently loses its oldest data.

**Options:**
1. **Recommended:** store late samples normally. Age stops being a quarantine reason; only impossible times are (more than 5 minutes in the future, before the product's epoch). Inserts into old daily partitions cost extra merges, which ClickHouse handles; the rollup dirty-hour recompute (spec 06 §7) already copes with late rows and is rate-limited. The late window then only bounds how far back rollups are recomputed automatically; older buckets are recomputed by a slower backfill job from raw rows.
2. Store late samples in a separate `readings_late` table and union it in queries. Keeps hot partitions clean; every query and rollup gets more complex.
3. Keep quarantine but make it queryable and auto-promote. Same storage as option 1 with more moving parts.

A sample older than its own retention period on arrival is the one honest exception: it is acked and deliberately not stored, counted as `expired_on_arrival` in metering and reconciliation, so it is declared, never silent.

### C30. Boot anchors for uptime backlogs (High, Decision)

**Found by:** customer scenarios X2 (scenarios 1, 6, 9, 11). **A defect in these specs (C1's fix is incomplete).**

**Problem:** spec 06 §4.3 anchors a boot from a `time_sync` in the same envelope, else `received_at − (t0 + max dt)` of the first envelope seen for that boot, first-write-wins. With oldest-first backlog draining (spec 01 §7.5) the first envelope seen for an old boot usually has no `time_sync` and arrives days late, so the whole boot is stamped days late, and the anchor can never be corrected because `ts` is in the storage key (I5).

**Options:**
1. **Recommended, two parts.** (a) Device: once a boot has synced, every later envelope carrying uptime samples of that boot also carries that boot's `time_sync` event (its sync point is persisted with the boot record). Then every boot that ever synced is anchored exactly, whatever order its data arrives in. (b) Cloud: for a boot that never synced, the anchor is an estimate; store it with a distinct quality flag `anchor_estimated` (separate from `uptime_anchored`) and record the bounds known (previous boot's last synced time, first receive time), so queries and users can see the uncertainty. Ingest-side cost: one extra event per uptime envelope.
2. Take `ts` out of the storage keys for uptime samples (key on `inc, series, seq`) so anchors can be corrected later with a version column. Fixes everything, but changes the key-purity design (I5) and every table's ORDER BY; much larger change.
3. Defer anchoring until a `time_sync` arrives. Data from never-synced boots would wait forever or need a timeout, and rows would be invisible meanwhile.

### C31. Row volume driven by channel count (Medium, Decision after benchmarks)

**Found by:** customer scenarios X3 (scenarios 2, 4, 8, 10, 12).

**Problem:** `readings` holds one row per channel value, and spec 06 §4 also writes one `readings` row per block sample. Wide devices (220–670 channels) and waveforms put single customers at tens of billions of rows a day, changing the hardware estimate by an order of magnitude.

**Options:**
1. **Recommended now:** block samples are stored only in `blocks` (one row per block, values as a compressed array), not in `readings`. Cheap to decide before code exists.
2. **To benchmark before deciding:** keep narrow `readings` (simple queries, best compression per column) versus a row per record with parallel arrays or a `Map(UInt16, Float64)` of channel values (far fewer rows, harder per-channel queries and rollups). The research thread's pipeline area is asked to compare these with measured ClickHouse numbers for scenarios 8 and 10.
3. Per-channel storage tiers: channels flagged `diagnostic` go to a cheaper, shorter-lived table.

