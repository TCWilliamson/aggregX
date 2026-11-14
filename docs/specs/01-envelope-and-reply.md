# 01. Envelope and reply

Status: Draft 1. Normative grammar: `spec/wire/envelope.cddl`. Reference encoder: `internal/wire` (Go).

This spec defines what a device sends on every upload (the **envelope**) and what it gets back (the **reply**). The same envelope travels on both paths into the platform, direct HTTPS from WiFi devices and through a gateway from Thread devices, and on both it is wrapped in a COSE_Sign1 signature by the device key (spec 02 §3, spec 03 §4).

## 1. Goals and non-goals

Goals: one envelope for every product; small enough for 802.15.4 frames; exactly-once storage built from at-least-once delivery; every upload doubles as a twin, OTA and time sync point; ingest can do all its checks from the header without decoding the body.

Non-goals (v1): positional (schema-ordered) value encoding, delta compression between records, compression of the CBOR. All three are possible later behind a new envelope version `v`.

## 2. Encoding

- CBOR (RFC 8949). Maps use **small unsigned integer keys**; the names in this document are for humans only and never go on the wire.
- Encoders MUST use deterministic encoding for integers, lengths and map key order (RFC 8949 §4.2.1). Floats SHOULD use the shortest type that holds the value exactly. The Go reference encoder is byte-exact against the golden vectors; other encoders are checked by decoded value.
- Decoders MUST accept any well-formed encoding and MUST reject: duplicate map keys, nesting deeper than 16 levels, trailing bytes, and envelope keys above 17. Unknown keys inside a body section are passed to the workers, which quarantine (spec 04 §7).
- Content type of a direct upload: `application/cose; cose-type="cose-sign1"` (a signed batch). Gateway relay requests are `application/cbor` (spec 02 §4).
- Floats: encoders SHOULD use the shortest float that round-trips the value (half, single, double). Decoders MUST accept all three.

## 3. Envelope

| Key | Name | Type | Req | Meaning |
|---:|---|---|:-:|---|
| 0 | `v` | uint | ✓ | Envelope version. This spec is `1`. |
| 1 | `dev` | tstr | ✓ | Device serial, 1–32 chars `[A-Z0-9-]`. Must match the signing key's device (spec 02 §3.1). |
| 2 | `inc` | uint | ✓ | Incarnation, 32-bit, non-zero (§5.3). |
| 3 | `boot` | uint | ✓ | Boot counter, 32-bit, incremented on every boot and persisted. |
| 4 | `schema` | tstr | ✓ | Product schema the main records use, `name@major.minor`, e.g. `envsense@3.2` (spec 04). |
| 5 | `comp` | uint | ✓ | Composition epoch (spec 04 §5). `0` for products without slots. |
| 6 | `fw` | tstr | ✓ | Running firmware version, semver. |
| 7 | `cfg` | bstr(8) | ✓ | First 8 bytes of SHA-256 of the device's canonical reported config (spec 04 §4.3). |
| 8 | `tb` | uint | ✓ | Timebase: `0` synced (Unix ms), `1` uptime (ms since boot number `boot`). |
| 9 | `t0` | uint | ✓ | Batch base time in the timebase `tb`. Every time in the envelope is `t0 + dt`. |
| 10 | `fl` | uint |  | Flags. bit 0 `backfill` (this batch is older backlog, not live data); bit 1 `more` (the outbox holds more after this batch). Absent = 0. |
| 11 | `r` | section |  | Main records, series 0 (§4.1). |
| 12 | `mod` | map |  | Module sections keyed by slot number 1–239, series `16 + slot` (§4.2). |
| 13 | `blk` | section |  | High-rate blocks, series 2 (§4.3). |
| 14 | `ev` | section |  | Events and sessions, series 1 (§4.4). |
| 15 | `mres` | array |  | Mailbox item results (§7.3). |
| 16 | `rcfg` | map |  | Full reported config, sent after a `report_config` item or on change (spec 04 §4). |
| 17 | `n` | uint |  | Request nonce, 32-bit. Required on signed batches (spec 02 §3.4). Echoed in the reply. |

An envelope MUST contain at least one of `r`, `mod`, `blk`, `ev`, `mres`, `rcfg`. An envelope with only `mres` and/or `rcfg` is a **control upload** and carries no samples.

> **Design note (C5).** The design doc numbers "samples" with one counter and says records in an upload are consecutive, gives each module its own series, but does not say how events and high-rate blocks are numbered. This spec gives every kind of sample its own series. See CONTRADICTIONS.md C5.

### 3.1 Header vs body

Keys 0–10 and 17 are the **header**. Ingest parses only the header plus the first sequence number and length of each section (enough for de-duplication, §6) and never decodes values. Workers decode everything.

## 4. Sections and samples

A **sample** is one record, one block or one event. Every sample has a sequence number in exactly one series. A section is a CBOR array `[first_seq, [items…]]`: the items are consecutive samples `first_seq, first_seq+1, …` in that series. Resends MAY re-batch samples differently; de-duplication is by `(dev, inc, series, seq)` and does not care how samples were grouped.

| Series id | Kind | Where in the envelope |
|---:|---|---|
| 0 | Main records | `r` |
| 1 | Events | `ev` |
| 2 | High-rate blocks | `blk` |
| 3–15 | Reserved | |
| 16 + s | Records of the module in slot `s` (1 ≤ s ≤ 239) | `mod[s]` |

### 4.1 Records (`r`)

`r = [first_seq, [record…]]`, `record = [dt, {channel_id: value, …}]`

- `dt`: uint, milliseconds from `t0`. Records need not be in time order (clocks jump) but MUST be in sequence order.
- Channel ids come from the schema named in `schema` (spec 04). Channel ids ≥ 32768 are platform channels (`sys` schema: battery, RSSI, outbox depth…) and are valid in any product.
- Values: int, float, bool, tstr, or geo `[lat, lon]` / `[lat, lon, alt_m, acc_m]`. Enum channels send the enum's uint. A channel with no reading is omitted; `null` is not allowed.
- A record with an empty value map is allowed (it holds the sequence number, e.g. a heartbeat).

### 4.2 Module sections (`mod`)

`mod = {slot: [first_seq, [record…], sn, schema]}`

- `slot`: uint 1–239, declared in the product schema.
- `sn`: tstr, the module's serial, as printed on it.
- `schema`: tstr, the module schema `name@major.minor`.
- Records are as in §4.1, with channel ids from the module schema.
- The series id is `16 + slot`. The **host** device owns this counter, not the module, and it continues across module swaps in that slot. The serial in each section is what attributes a record to the right physical sensor (spec 04 §5); a backlog that spans a swap therefore arrives as separate envelopes or sections, each with the right serial.
- An envelope MUST NOT mix records from two different modules in one slot section. A swap mid-batch closes the batch.

### 4.3 High-rate blocks (`blk`)

`blk = [first_seq, [block…]]`, `block = {0: ch, 1: slot, 2: dt, 3: hz, 4: [v…]}`

- `ch`: channel id; `slot`: 0 for the main board, else the module slot (whose serial and schema come from the `mod` section in the same envelope or the most recent composition event).
- `dt`: start of the first sample, ms from `t0`. `hz`: sample rate, uint or float, > 0, ≤ 20,000. Sample `i` was taken at `t0 + dt + i × 1000 / hz` ms.
- `v`: 1–4096 numeric values.
- One block is one sample: one sequence number in series 2.

### 4.4 Events (`ev`)

`ev = [first_seq, [event…]]`, `event = {0: type, 1: dt, 2: dur, 3: {attr_id: value}, 4: slot}`

- `type`: uint event type. 1–999 are platform events (§4.5), 1000+ come from the product or module schema.
- `dt`: start time, ms from `t0`. `dur`: duration in ms; absent for an instantaneous event.
- `attrs`: optional map of attribute id to value (same value types as records).
- `slot`: optional, the module slot the event belongs to (0 or absent = main board).
- **Events are immutable and sent once, complete.** A session (gym use, burst session) is sent when it ends, with `dt` and `dur`. A session longer than the reporting interval MAY additionally send `session_progress` events (type 9) so live views see it; storage treats them as separate events.

> **Design note (C15).** The design doc keys events on (stream, type, start, seq) but never says whether a session is sent once or opened and closed with two messages. Two messages would leave two rows. This spec chooses "sent once, complete".

### 4.5 Platform event types

| Type | Name | Attributes |
|---:|---|---|
| 1 | `boot` | 1 reason (uint: 0 power-on, 1 brownout, 2 watchdog, 3 panic, 4 OTA, 5 user), 2 reset register (uint) |
| 2 | `boot_skip` | 1 series, 2 from seq, 3 to seq (inclusive). Numbers deliberately never used (§5.2). |
| 3 | `dropped` | 1 series, 2 from, 3 to (inclusive), 4 reason (0 outbox full, 1 thinned, 2 malformed-rejected, 3 corrupt flash). Samples the device had and lost. |
| 4 | `burst` | 1 trigger id, 2 sample rate. With `dur`. Blocks inside `[dt, dt+dur]` belong to it. |
| 5 | `composition` | Map slot → `[module_type tstr, sn tstr, schema tstr]`. Sent whenever `comp` changes. |
| 6 | `time_sync` | 1 uptime ms, 2 Unix ms, 3 source (0 cloud reply, 1 gateway, 2 SNTP, 3 GNSS). Sent once per boot when the clock is first set. |
| 7 | `clock_jump` | 1 old Unix ms, 2 new Unix ms. |
| 8 | `config_local` | 1 config version, 2 new hash (bstr). Config changed on the device (button, tool). |
| 9 | `session_progress` | Product-defined, see §4.4. |
| 10 | `ota_progress` | 1 artifact id, 2 state (spec of OTA, Phase 1), 3 percent. |

## 5. Sequence numbers, incarnation and boot

### 5.1 Rules

1. Each series is a uint32 counter. A new incarnation starts every series at 0.
2. A device assigns the next number at capture time, never at upload time.
3. A sample keeps its number, its timestamp and its timebase forever. It MUST NOT be renumbered or re-stamped, even after the clock is set (§8).
4. If any counter would wrap past 2³²−1 the device MUST start a new incarnation.

### 5.2 Surviving reboots

The device persists a high-water mark per series every 1,000 numbers (the "block"). On boot, for each series, it resumes at `max(persisted_block + 1000, highest seq found in the outbox + 1)` and sends one `boot_skip` event per series covering the numbers it skipped. Gaps inside a `boot_skip` range are classified "skipped", not "lost" (spec 06 §6). Data that later arrives inside a skipped range is still accepted.

> **Design note (C23).** The design doc resumes only from the persisted block. Scanning the outbox as well keeps unsent samples from colliding with new numbers if the counter was saved late.

### 5.3 Incarnation

`inc` is a random non-zero 32-bit value generated on first boot and **regenerated whenever the device cannot prove its counters are intact**: factory reset, flash wipe, counter storage corrupt, counter wrap. It is persisted alongside the counters. De-duplication, acks and routing all key on `(dev, inc, series, seq)`.

> **Design note (C13).** Without this, a reflashed device restarts its counters at 0 and every new sample collides with an old one: the platform would acknowledge real data as duplicates and discard it. The design doc does not cover this case.

## 6. What ingest checks in the envelope

Ingest parses the header and, for each section, `first_seq` and the item count. It does not decode values. Its checks and their order are in spec 03 §6. The envelope-level ones:

- `v` is supported; required keys present; no unknown header keys.
- `dev` equals the authenticated device.
- `t0` and `tb` are plausible: for `tb = 0`, `t0` is not more than 5 minutes in the future and not older than the product's late-data window (otherwise accept-and-quarantine, never reject: §9).
- Section sizes within limits (§10).

## 7. Reply

Every successful upload, and every gateway poll (spec 02 §4), gets a reply.

| Key | Name | Type | Req | Meaning |
|---:|---|---|:-:|---|
| 0 | `now` | uint | ✓ | Server time, Unix ms, taken just before the reply is sent. |
| 1 | `dev` | tstr | signed | Device serial. Required in signed replies so a reply cannot be replayed to another device. |
| 2 | `inc` | uint | ✓ | The incarnation the acks refer to. A device ignores acks for another incarnation. |
| 3 | `n` | uint |  | Echo of the request nonce `n`. |
| 4 | `st` | uint | ✓ | Device status: 0 ok, 1 throttled, 2 unclaimed, 3 revoked, 4 retry later, 5 rejected. |
| 5 | `ack` | map |  | Per series: `[upto, [[from, to]…]]`. |
| 6 | `resend` | map |  | Per series: `[[from, to]…]`. Ranges the server wants resent. |
| 7 | `next` | uint | ✓ | Seconds until the next upload. Pacing and backpressure. |
| 8 | `mbox` | array |  | Mailbox items (§7.2), in ascending id order. |
| 9 | `more` | bool |  | More mailbox items are waiting; the device SHOULD upload or poll again soon. |

> **Design note (C3).** The design doc's reply has one `ack` and one `resend`, but devices have several series. Acks and resends here are per series.

### 7.1 Acks

- `upto` (int): every sequence number from 0 up to and including `upto` is **accepted or declared** by the device (covered by a `boot_skip` or `dropped` event). `−1` means nothing is contiguous yet. The server never advances `upto` over a gap the device has not declared, so a device never deletes a sample because the server gave up on it.
- `ranges`: always present, possibly empty; up to 16 inclusive ranges above `upto` that are also accepted, those overlapping the request first.
- A device deletes a sample from its outbox if and only if its `(inc, series, seq)` is covered by `upto` or a range in a reply for its own `inc`. It MUST NOT delete on any other signal (a 200 status, a gateway's word, a timeout).
- Acks are monotonic: a later reply never acknowledges less. Devices therefore MAY process replies in any order, and a replayed old reply is harmless.
- The server includes in `ack` every series that the request touched, and MAY include others.

### 7.2 Mailbox items

`item = {0: id, 1: type, 2: exp, 3: body, 4: prio}`

- `id`: uint, **strictly increasing per device** across all item types, starting at 1. Never reused, even across incarnations.
- `type`: see the table. `exp`: Unix ms after which the device MUST NOT act on it. `prio`: 0 normal (default), 1 high.
- The device processes items in `id` order and persists `last_id`, the highest id it has processed. It rejects any item with `id ≤ last_id`. This, plus `exp`, is what makes a replayed reply harmless (spec 02 §5.3).

> **Design note (C25).** The design doc shows mailbox ids as strings (`mb_19`). Replay protection needs a per-device monotonic integer.

| Type | Name | Body | Device action |
|---:|---|---|---|
| 1 | `cfg` | `{0: ver, 1: hash bstr(8), 2: {key: value}, 3: [key…]}` (set, unset) | Apply the changes atomically. Report `applied` once its config hash equals `hash`, else `failed` with reason. Keys are config field ids: `slot × 65536 + field_id`, slot 0 = main board (spec 04 §4). |
| 2 | `cmd` | `{0: command_id, 1: {arg_id: value}}` | Run the command from the schema. |
| 3 | `ota` | `{0: artifact_id tstr, 1: version tstr, 2: size, 3: sha256 bstr(32), 4: url tstr, 5: target, 6: conditions}` | Download, verify signature, install. `url` is absent on the gateway path; the gateway serves the image (spec 02 §6). `target`: 0 main MCU, 1 radio, 16+slot module. Full OTA spec is Phase 1. |
| 4 | `report_config` | `{}` | Send `rcfg` in the next upload. |
| 5 | `move_boundary` | `{0: move_id}` | See §7.4. |
| 6 | `live` (Later) | `{0: sample_ms, 1: report_s, 2: lease_s}` | Temporary interval override that expires on the device. Hook only. |
| 7 | `key_rollover` | `{0: kid bstr, 1: public key bstr(65), 2: not_before}` | Add a cloud reply-signing key (spec 02 §5.2). |
| 8 | `unclaim` | `{0: reason}` | Wipe config to factory defaults; keep identity, counters and `inc`. |
| 9–63 | Reserved | | Device reports `rejected` (unsupported). |

### 7.3 Mailbox results (`mres`, in the next upload)

`mres = [[id, status, detail?]…]`

| Status | Meaning |
|---:|---|
| 0 | received (optional, for long-running items such as OTA) |
| 1 | applied |
| 2 | failed; `detail` = error code (uint) or short text |
| 3 | rejected; `detail`: 0 expired, 1 replay, 2 unsupported type, 3 invalid body |
| 4 | deferred (e.g. OTA conditions not met); the device will retry |
| 5 | already processed; the device no longer remembers the outcome |

The device keeps the outcome of its last 8 items so it can answer a server that re-sends an item whose result was lost. For older ids it answers `5`.

### 7.4 Move boundary

When the device processes a `move_boundary` item it, atomically and between two samples:

1. Records `boundary[series] = next sequence number it will assign`, for every series it has (main, events, blocks, every populated slot).
2. Reports `[id, 1, {series: boundary_seq, …}]` in `mres`.

Every sample numbered below the boundary was captured before the move; every sample at or above it, after. The platform routes accordingly (spec 05 §5). The device does nothing else: it keeps its outbox and sends old samples as usual. Config reset and credential rotation that go with a move arrive as separate items.

> **Design note (C4).** The design doc has the device acknowledge "with its current sequence number": one number. With several series the boundary is a vector.

### 7.5 Draining a backlog

A device that has been offline (no network, gateway down, platform refusing writes) may hold days or months of samples, in internal flash or on external storage such as an SD card. It drains them with ordinary uploads; there is no bulk-transfer mode.

1. **Batches stay normal-sized.** Each upload is bounded by the transport's size limit (§10): about 1 KiB signed over Thread, up to the product's direct limit (16 KiB recommended, 64 KiB hard) over WiFi. A lost upload costs one batch, never the backlog.
2. **Live first, then oldest first.** Each upload carries the newest unsent samples first, then fills the remaining space with the oldest unacknowledged samples. A batch that holds only backlog sets `fl.backfill`; any batch sent while more is waiting sets `fl.more`.
3. **The cloud sets the pace.** The device sends its next batch after the reply's `next` seconds. `next = 0` means send again immediately. While it has capacity the platform answers backfill batches with `next = 0`; under load it stretches `next` for backfill batches before live ones (spec 03 §6.1, admission control). Without a reply (offline, timeout) the device backs off exponentially with jitter, from 10 s up to its normal interval.
4. **Storage size never affects RAM.** The device encodes a batch record by record directly from storage (spec 04 §8.4), so a 32 GB card needs the same RAM as a 1 MB outbox. Only the sample index (series, first and last seq per stored block) is kept in RAM or rebuilt by scanning at boot.
5. **Depth is visible.** The device reports outbox depth and oldest unsent sample age in `sys` channels on every upload, so the portal can show how far behind a device is.
6. **Acks free storage in order.** Acks and ranges (§7.1) let the device free whole storage blocks once every sample in them is covered; it never needs to rewrite storage in place.

Allowances must not strand a backlog: backfill batches are counted against a separate backfill volume allowance, sized for a product's maximum offline period, and over-allowance backfill is paced with `next`, never refused (spec 03 §6.1).

## 8. Time

- **Synced** (`tb = 0`): `t0 + dt` is the capture time in Unix ms by the device's clock.
- **Uptime** (`tb = 1`): `t0 + dt` is milliseconds since boot number `boot`. An envelope MUST NOT mix boots or timebases; the device closes the batch instead.
- Devices set their clock from a reply's `now` (spec 02 §3.5 for the gateway path), from the gateway's CoAP time, from SNTP or GNSS. When a device first sets its clock in a boot it SHOULD send a `time_sync` event.
- Samples captured before the clock was set stay in the uptime timebase forever (§5.1 rule 3).
- **Anchoring.** The platform converts uptime times to wall-clock times with one anchor per `(dev, inc, boot)`, fixed the first time a worker sees that boot and never changed afterwards (spec 06 §4.3). The anchor comes from a `time_sync` event in that same envelope if there is one, otherwise from `received_at − (t0 + max dt)`. Anchored readings carry the `uptime_anchored` quality flag.

> **Design note (C1).** The design doc anchors uptime timestamps "to the receive time". If each upload were anchored to its own receive time, a resent sample would get a different timestamp, and since the timestamp is part of the storage de-duplication key, duplicates would never merge. The anchor must be fixed per boot. This is the most serious defect found; see CONTRADICTIONS.md C1.

## 9. Errors and HTTP status (direct path)

Device-level outcomes are always in the reply's `st`. HTTP status is the transport outcome.

| HTTP | `st` | When | Device action |
|---|---|---|---|
| 200 | 0 | Accepted (or all duplicates) | Process acks and items |
| 400 | — | Not CBOR, unknown `v`, missing or unknown header key, `dev` malformed | Do not retry the same bytes more than 3 times. Then discard the batch and record a `dropped` event (reason 2). |
| 401 | — | Body is not a COSE_Sign1, or its signature does not verify against a key of the device | Firmware or key fault: as 400 |
| 403 | 2 or 3 | Unclaimed or revoked | Keep data. Retry after `next` (long). |
| 413 | — | Over size limit | Split and retry |
| 415 | — | Content type is not `application/cose; cose-type="cose-sign1"` | Firmware bug: as 400 |
| 429 | 1 | Allowance exceeded | Keep data. Wait `next`. |
| 503 | 4 | Platform cannot accept writes (stream buffer down) | Keep data. Wait `Retry-After` with jitter. |

Things that are **never** rejected at ingest, because rejecting them would leave data stuck in a device outbox forever: unknown schema or schema version, unknown channel, out-of-range value, too-old or future timestamp. Ingest accepts and acknowledges them; workers quarantine or flag them (spec 04 §7, spec 06 §5).

> **Design note (C11).** The design doc's ingest step 4 rejects "schema not allowed for the product". This spec moves that to accept-and-quarantine.

## 10. Size limits

| Limit | Value | Notes |
|---|---|---|
| Direct envelope | 64 KiB hard, 16 KiB recommended | Per-product setting below the hard limit |
| Signed envelope over Thread | 1,024 bytes recommended, 4 KiB hard | Fits 9–10 802.15.4 frames at 1,024. Larger uses CoAP block-wise to the gateway. |
| Gateway relay request | 1 MiB, ≤ 500 batches | spec 02 §4 |
| Items per section | ≤ 1,000 | |
| Values per block | ≤ 4,096 | |
| Ranges per series in `ack` | ≤ 16 | `resend` ≤ 8 |
| Mailbox items per reply | ≤ 8, total body ≤ 2 KiB on the gateway path | `more` signals the rest |
| `rcfg` | ≤ 8 KiB | |

## 11. Worked examples

Values below were encoded by the Go reference encoder in `internal/wire` and are stored as golden vectors in `spec/vectors/` (with a `.diag` file of each in diagnostic notation). Sizes are exact. Floats are sent as the shortest IEEE type that holds the device's value: 21.4 is a float32 (decoders show 21.3999996), 19.75 fits a float16.

### 11.1 Minimal direct upload: two records

A synced device, schema `envsense@3.2`, channels 1 (temperature) and 2 (humidity), samples 18233 and 18234 five seconds apart.

```
{                         / envelope /
  0: 1,                   / v /
  1: "SN-00042",          / dev /
  2: 2876543210,          / inc /
  3: 17,                  / boot /
  4: "envsense@3.2",      / schema /
  5: 0,                   / comp /
  6: "2.4.1",             / fw /
  7: h'A91F03C24D5E6F70', / cfg /
  8: 0,                   / tb: synced /
  9: 1794614900123,       / t0 /
  11: [18233, [           / r: first seq, records /
        [0,    {1: 21.4, 2: 45}],    / 21.4 sent as float32 /
        [5000, {1: 21.5, 2: 45}]
      ]]
}
```

Encoded, 96 bytes:

```
ab00010168534e2d3030303432021aab7490ea0311046c656e7673656e736540
332e3205000665322e342e310748a91f03c24d5e6f700800091b000001a1d762
359b0b82194739828200a201fa41ab333302182d82191388a201f94d6002182d
```

### 11.2 Thread device with a module, an event and a block

Main records in series 0, a CO₂ module in slot 1 (series 17), a `boot` event (series 1) and a 100 Hz accelerometer block (series 2). This is the payload that spec 02 §3 signs.

```
{
  0: 1, 1: "SN-00107", 2: 912345678, 3: 4, 4: "envsense@3.2", 5: 7,
  6: "2.4.1", 7: h'00112233AABBCCDD', 8: 0, 9: 1794614903000, 17: 3141592653,
  11: [5120, [[0, {1: 19.75, 32768: 3012}]]],          / r: temp, sys.battery_mv /
  12: {1: [901, [[0, {1: 612}]], "M-7781", "co2@2.0"]}, / mod: slot 1 /
  13: [44, [{0: 9, 1: 0, 2: 120, 3: 100, 4: [0.25, -0.125, 0.5]}]], / blk /
  14: [300, [{0: 1, 1: 0, 3: {1: 4}}]]                  / ev: boot, reason OTA /
}
```

Encoded, 163 bytes:

```
af00010168534e2d3030313037021a36614a4e0304046c656e7673656e736540
332e3205070665322e342e31074800112233aabbccdd0800091b000001a1d762
40d80b82191400818200a201f94cf0198000190bc40ca10184190385818200a1
01190264664d2d3737383167636f3240322e300d82182c81a500090100021878
0318640483f93400f9b000f938000e8219012c81a30001010003a10104111abb
40e64d
```

### 11.3 Reply with acks, a resend request and a config item

```
{
  0: 1794614960500,                       / now /
  2: 2876543210,                          / inc /
  4: 0,                                   / st: ok /
  5: {0: [18233, [[18300, 18320]]]},      / ack series 0 /
  6: {0: [[18234, 18299]]},               / resend series 0 /
  7: 60,                                  / next /
  8: [{0: 19, 1: 1, 2: 1794618560500,     / mbox: id 19, cfg, exp /
       3: {0: 12, 1: h'5E0C1D2E3F405162', 2: {3: 5}}}]  / ver 12, hash, set sample_s=5 /
}
```

Encoded, 83 bytes:

```
a7001b000001a1d7632174021aab7490ea040005a10082194739818219477c19
479006a100818219473a19477b07183c0881a400130101021b000001a1d79a0f
f403a3000c01485e0c1d2e3f40516202a10305
```

### 11.4 Uptime batch after a reboot with no clock yet

```
{
  0: 1, 1: "SN-00107", 2: 912345678, 3: 5, 4: "envsense@3.2", 5: 7,
  6: "2.4.2", 7: h'00112233AABBCCDD', 8: 1, 9: 2150, 17: 77,
  11: [6000, [[0, {1: 19.5}], [5000, {1: 19.5}]]],
  14: [301, [{0: 1, 1: 0, 3: {1: 4}},
             {0: 2, 1: 0, 3: {1: 0, 2: 5121, 3: 5999}}]]   / boot_skip series 0 5121..5999 /
}
```

The samples are at 2,150 ms and 7,150 ms after boot 5. The first worker to see boot 5 of this incarnation fixes its anchor; both records get `ts = anchor + 2150` and `anchor + 7150` for ever, however many times they are resent.

Encoded, 117 bytes:

```
ad00010168534e2d3030313037021a36614a4e0305046c656e7673656e736540
332e3205070665322e342e32074800112233aabbccdd0801091908660b821917
70828200a101f94ce082191388a101f94ce00e8219012d82a30001010003a101
04a30002010003a30100021914010319176f11184d
```

### 11.5 Move boundary round trip

Reply item: `{0: 31, 1: 5, 2: 1795219760500, 3: {0: 88}}` (id 31, `move_boundary`, move 88).

Next upload (a control upload):

```
{ 0: 1, 1: "SN-00107", 2: 912345678, 3: 5, 4: "envsense@3.2", 5: 7,
  6: "2.4.2", 7: h'00112233AABBCCDD', 8: 0, 9: 1794700000000, 17: 78,
  15: [[31, 1, {0: 6230, 1: 302, 2: 45, 17: 1022}]] }   / boundary per series /
```

Samples numbered below 6230 in series 0, 302 in series 1, 45 in series 2 and 1022 in series 17 stay with the old owner, whenever they arrive.

Encoded, 91 bytes:

```
ac00010168534e2d3030313037021a36614a4e0305046c656e7673656e736540
332e3205070665322e342e32074800112233aabbccdd0800091b000001a1dc74
bb000f8183181f01a4001918560119012e02182d111903fe11184e
```
