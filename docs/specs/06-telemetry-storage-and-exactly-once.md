# 06. Telemetry storage and the exactly-once contract

Status: Draft 1. Tables: `migrations/clickhouse/0001_telemetry.sql`, `migrations/postgres/0002_pipeline.sql`.

Devices deliver **at least once**: they resend until acknowledged. The platform stores **exactly once**, as observed by any query that asks for exact results. This spec defines the path from ingest to ClickHouse, the invariant that makes it exact, and how to check it.

## 1. The contract

> For every sample a device deletes from its outbox because of a cloud acknowledgement, the platform holds that sample durably in Redpanda (I2), will hold it in ClickHouse exactly once as seen through `FINAL` (I4–I6) and in the archive at least once (I8). ClickHouse holds no sample that a device did not send.

The invariants below, numbered I1–I8, are what the code must preserve. Each is tested (spec 07).

| # | Stage | Invariant |
|---|---|---|
| I1 | Device | A sample stays in the outbox until a verified cloud ack covers its `(inc, series, seq)` (spec 01 §7.1). |
| I2 | Ingest | Ingest acknowledges a sequence number only after the batch containing it has been acknowledged by Redpanda with `acks=all`. |
| I3 | Ingest | Ingest skips publishing a batch only when Redis **positively** records every sequence number in it as accepted. Any doubt (state missing, evicted, Redis restarted) means publish. |
| I4 | Workers | A worker commits a Redpanda offset only after every side effect of every message up to it is durable: ClickHouse inserts, PostgreSQL transaction, Redis updates. |
| I5 | Storage | Every column in a ClickHouse partition key or sorting key is a pure function of the raw batch, its receive time, and first-write-wins registry facts. So a replayed or resent sample produces a byte-identical key and lands in the same partition. |
| I6 | Storage | Exact queries read with `FINAL` (or an equivalent `GROUP BY` on the full key). |
| I7 | Rollups | A rollup is always recomputed from raw rows read with `FINAL`, never incremented. A bucket is marked dirty in the same PostgreSQL transaction that precedes the offset commit. |
| I8 | Archive | The archive consumer commits an offset only after the object containing that message is written. |

At-least-once from the device plus I2–I4 gives "every acknowledged sample reaches ClickHouse at least once". I5 and I6 turn that into "exactly once when read". I3 is what stops the Redis optimisation from ever losing data.

## 2. Redpanda

| Topic | Key | Value | Partitions | Retention |
|---|---|---|---|---|
| `iot.ingest.v1` | device serial | Ingest record (§2.1) | 48 | 7 days |

- Producer: `acks=all`, idempotent producer on, zstd compression, max message 1.5 MiB.
- Pilot: one broker, with fsync on every write (Redpanda's default), on mirrored NVMe. From 10k: three brokers, replication factor 3, `min.insync.replicas=2`.
- **Key = device serial**, so all of a device's batches are in one partition, in the order ingest accepted them. Workers process them in that order.

> **Design note (C7).** The design doc keys the stream by data stream id. Ingest cannot know the stream for every sample (routing is per sample, by sequence number, spec 05 §5), and an isolated move splits one device's data across two streams mid-flight. Keying by device keeps every device's history in order, which is what routing needs.

### 2.1 Ingest record

CBOR map, written by ingest, read by workers and the archive:

| Key | Name | Type | Meaning |
|---:|---|---|---|
| 0 | `v` | uint | 1 |
| 1 | `received_at` | uint | Unix ms when ingest received the request |
| 2 | `node` | tstr | Ingest instance id |
| 3 | `path` | uint | 0 direct, 1 gateway |
| 4 | `gw` | tstr | Gateway serial, gateway path only |
| 5 | `dev` | tstr | Device serial |
| 6 | `device_id` | uint | Internal device id |
| 7 | `batch` | bstr | The batch exactly as received: COSE_Sign1 or plain envelope |
| 8 | `known` | map | Per series, ranges ingest already knew were accepted (a hint; workers do not rely on it) |

The raw batch is kept as received, signature included, so the archive is evidence of what each device actually signed.

## 3. Ingest sequence state (Redis)

One Redis hash per `(device_id, inc, series)`, key `seq:{device_id}:{inc}:{series}`, TTL 35 days (renewed on use):

| Field | Meaning |
|---|---|
| `c` | Contiguous high-water: every seq ≤ `c` is accepted or device-declared (`boot_skip`, `dropped`). Starts at −1. |
| `r` | Accepted or declared ranges above `c`, merged, as a compact list. At most 64 ranges. |
| `u` | Uncertainty floor. Starts at −1. |

A sequence number `s` is **known accepted** if `s ≤ c`, or `s` lies in a range in `r`. If `r` would exceed 64 ranges, the lowest range is dropped and `u` set to its upper end; numbers in `(c, u]` not in `r` are then "unknown" and are published if they arrive. Expiry of the whole key makes everything unknown. Unknown always means publish (I3); storage de-duplication absorbs it.

### 3.1 Per request

1. **Check** (one pipelined round trip with the allowance check): for each section `[first, last]`, is every number known accepted?
2. If every section is fully known accepted, the batch is a duplicate: do not publish; reply with acks.
3. Otherwise publish the whole batch to Redpanda and wait for its ack. (Partial overlaps are published whole; storage de-duplicates the overlap.)
4. **Mark**: a Lua script merges each section's range into `r` and advances `c` across any now-contiguous ranges.
5. **Reply**: `ack[series] = [c, ranges]`, where `ranges` are the ranges in `r` that overlap this request first, then the lowest others, at most 16 (spec 01 §7.1). `resend[series]` = holes between `c` and the lowest number in this request, at most 8.

Workers call a **declare** script when they decode `boot_skip` or `dropped` events, adding the declared range to `r` (and advancing `c`), so acks can move past numbers the device says it never used or no longer has.

Redis unavailable: ingest returns 503 (devices keep their data). Ingest never acknowledges without the round trip.

Two ingest pods handling the same device's retries at once may both publish. Storage absorbs it.

## 4. Worker pipeline

Consumer group `wk-store` on `iot.ingest.v1`. Per message, in partition order:

1. Decode the ingest record and the envelope. (The signature was verified by ingest; Redpanda is internal.)
2. Resolve, using caches backed by PostgreSQL, each with first-write-wins upserts:
   - `inc_ordinal` (spec 05 §5),
   - for an uptime batch (a boot that never synced), route its samples to `readings_unanchored` (§4.3),
   - module and sensor instance ids (spec 04 §5),
   - if `mres` contains an applied `move_boundary`: **flush the current batch** (steps 5–7), apply the move transaction (spec 05 §6.3), then continue,
   - the stream for each sample (spec 05 §5),
   - the retention class for each sample (§4.2).
3. Validate against the registry (spec 04 §7). Failures go to `quarantine`.
4. Build rows: `readings` (records and module records only), `blocks` (one row per block, values as a compressed array; block samples are **not** expanded into `readings`, decided 14 Nov, C31), `events`, `readings_unanchored` (§4.3); observations for gap tracking; dirty rollup hours; latest values; mailbox results; reported config.
5. Flush when 10,000 rows or 1 s have accumulated: insert into ClickHouse (synchronous inserts, `insert_deduplication_token = <table>:<partition>:<first offset>-<last offset>`).
6. Then one PostgreSQL transaction: gap state, `rollup_dirty`, mailbox results, twin reported state, `stream_schemas`, metering.
7. Then Redis: latest values (written only if newer than what is there), declared ranges.
8. Then commit the offset.

A crash at any step replays from the last committed offset. Every step is idempotent: ClickHouse by I5, PostgreSQL by upserts and idempotent state transitions, Redis by "newer wins" and set semantics.

The insert-deduplication token only saves merge work. Batch boundaries after a restart may differ from the first attempt, in which case the token does not match and the rows are de-duplicated by the engine instead. Correctness never depends on it.

### 4.1 The key-purity invariant (I5)

Every column in a partition key or sorting key must be computable, identically, on every replay:

| Column | Source | Why it is stable |
|---|---|---|
| `stream_id` | Routing by position (spec 05 §5) | Closed stream positions never change; ordinals are first-write-wins |
| `sensor_instance_id` | Upsert on (module serial, channel key) | Unique constraint returns the same id |
| `channel_id`, `series`, `seq`, `idx`, `event_type`, `slot` | The batch | Immutable bytes |
| `inc_ordinal` | First-write-wins upsert | Never updated |
| `ts`, `start_ts` | `t0 + dt` from the batch (always wall-clock; the device converts before first transmission, spec 01 §8) | Immutable bytes |
| `retention_class` | Stream's class effective at the sample's `ts` (§4.2) | Effective-dated rows; changes apply from a future time |

Any new key column must be added to this table with its justification, or the change is rejected in review.

### 4.2 Retention class

`stream_retention` holds effective-dated classes. Changing a stream's class closes the current row at a time in the future (default: next UTC midnight) and opens a new one. Samples are stamped with the class valid at their own `ts`, so a sample replayed after the change still gets the class it got the first time.

> **Design note (C2).** The design doc says the retention class is "a column on the stream". For TTL to drop whole parts, ClickHouse needs the class on the row and in the partition key; and if the class were read from the stream's current value, a replay after a class change would land in a different partition and never merge with its first copy.

### 4.3 Boot anchors

**Changed 14 Nov (C30).** Devices convert uptime timestamps to wall-clock time themselves before first transmission (spec 01 §8), so the platform no longer estimates anchors. An uptime (`tb = 1`) batch now means a boot that ended without ever syncing. Its samples are written to `readings_unanchored` (keyed by device, `inc`, `boot`, series, seq, idx; columns include `ts_dev` in uptime ms and `received_at`), not to `readings`. They are kept for the device's raw retention, visible per device and per boot, and excluded from timeline queries and rollups. They are counted in reconciliation as stored. *(Migration change pending: `readings_unanchored` replaces the boot-anchor path; `boot_anchors` is retired.)*

> **Design note (C1).** This is the fix for the most serious defect found. See spec 01 §8.

### 4.4 Quality flags

| Bit | Flag |
|---:|---|
| 0 | `device_converted` (captured before sync, converted by the device from its boot's sync point, spec 01 §8) |
| 1 | `out_of_range` |
| 2 | `clock_future` (synced ts more than 5 min after receive) |
| 3 | `backfill` (batch flag) |
| 4 | `routing_suspect` (spec 05 §5) |
| 5 | `gateway_time` (device clock set from unsigned gateway time this boot, spec 02 §5.4) |
| 6–15 | Reserved |

## 5. Quarantine and replay

Rows go to `iot.quarantine` with the raw batch, the source offset and a reason. After the schema or registry problem is fixed, a replay job feeds the raw batch back through the worker pipeline (same code path, same keys, so a partial earlier success merges cleanly) and records `replayed_at`. Quarantine reasons are a fixed list in code.

## 6. Gaps and loss

Workers track, per `(device, inc, series)`, the highest sequence number seen (`series_state`) and the holes below it (`series_gaps`):

| State | Meaning |
|---|---|
| `pending` | Hole seen; ingest asks for it in `resend` |
| `skipped` | Covered by a `boot_skip`: numbers never used |
| `dropped` | Covered by a `dropped` event: **known loss** |
| `unexplained` | Still pending after the gap timeout (default 24 h, and only once newer data from the same series has kept arriving): **unexplained loss**, a health signal |

A pending, skipped or unexplained hole that later fills is shrunk or deleted. Gap state is for reporting and resend requests only; it never decides what is stored.

Rollups carry `cnt`, so charts show gaps as breaks, not zeros. Completeness per device and group is computed from gap state.

## 7. Rollups

### 7.1 Tables and meaning

`rollup_1m`, `rollup_30m`, `rollup_1d`: per `(stream, sensor instance, channel, bucket)`: `cnt`, `sum`, `min`, `max`, `last`, `last_ts`, in **raw units** (calibration is applied on read, design doc "Sensor calibration"). Which of these a channel exposes is the schema's `agg`. `time_in_state` for enums needs a separate table and is Later (the `agg` value is the hook). Buckets are UTC; local-time views (site time zones) are built from 30-minute buckets, which is exact for every zone with a whole- or half-hour offset.

### 7.2 Recompute

- Step 6 of the worker upserts `rollup_dirty(stream_id, hour)` for every hour that received raw rows.
- A rollup worker selects dirty hours not marked in the last 60 s (`FOR UPDATE SKIP LOCKED`, up to 5,000 streams per hour bucket), and for each group:
  - `INSERT INTO rollup_1m SELECT … FROM readings FINAL WHERE stream_id IN (…) AND ts in [hour, hour + 1h) GROUP BY stream_id, sensor_instance_id, channel_id, toStartOfMinute(ts)`,
  - then the two 30-minute buckets of that hour from `rollup_1m FINAL`,
  - then the day from `rollup_30m FINAL`.
- It deletes the dirty row only if `marked_at` is unchanged, so data that arrived during the recompute triggers another.
- Recompute is rate-limited so backfill never stalls live inserts.

Rollup rows are versioned by `computed_at`; readers use `FINAL`.

## 8. Late data and retention

**Changed 14 Nov (C29): samples of any age are stored.** Age is never a reason to quarantine. Only impossible times are quarantined: more than 5 minutes in the future, or before the product's epoch.
- **Within the late window** (`retention_classes.late_window_days`, default 30 days): stored normally; the rollup dirty-hour recompute (§7) runs as usual.
- **Older than the late window but within raw retention:** stored normally in `readings`. Their rollup buckets are marked dirty in a separate low-priority queue, recomputed from raw rows by a slow late-path worker that is rate-limited so it never delays live data.
- **Older than the class's raw retention:** the raw row would expire on insert, so it is not written to `readings`. The batch is in the archive (§10) as for every batch; the late-path worker recomputes the affected rollup buckets (which have longer retention) from the archive. Counted in reconciliation as `expired_on_arrival`, a declared outcome, never silent.
- Inserting into old daily partitions costs extra merges; the slow path and its rate limit are what bound that cost. The 7-day, 45-day and 6-month backlog scenarios (spec 07 P6, customer scenarios 1, 6, 11) are the tests.
- Expiry is by TTL with `ttl_only_drop_parts`: partitions are one retention class and one day, so whole parts expire together and are dropped, never rewritten.
- Retention per tier (pilot): raw `standard` 400 days, raw `event` (bursts) and events indefinite, `rollup_1m` 400 days, `rollup_30m` 5 years, `rollup_1d` indefinite.

## 9. Reading exactly

- Exact queries use `FINAL`. With partitions by class and day and I5, duplicates are always in the same partition, so `do_not_merge_across_partitions_select_final = 1` is safe and keeps `FINAL` cheap.
- Dashboards that tolerate transient duplicates during merges MAY skip `FINAL` on rollups only.

## 10. Archive

- Consumer group `wk-archive` on `iot.ingest.v1` writes Parquet (zstd) to object storage every 5 minutes or 128 MiB: `raw/v1/date=YYYY-MM-DD/hour=HH/bucket=<hash(serial) % 16>/<partition>-<first offset>-<last offset>.parquet`.
- Columns: every field of the ingest record, plus partition and offset.
- It commits offsets only after the object is written (I8). A replay can write a second file holding the same messages; readers de-duplicate by `(partition, offset)`, and a compaction job merges files per hour.
- ClickHouse can be rebuilt from the archive by feeding the batches through the worker pipeline.

> **Design note (C8).** The design doc says the archive is partitioned "by stream and date" in one place and by time, ordered by stream, in another. The exporter reads raw batches from Redpanda, before any stream has been resolved, so it cannot partition by stream. This spec partitions by time and a device hash. A decoded, stream-attributed Parquet export is a separate later job if needed.

## 11. Reconciliation (Phase 0 exit check)

The simulator writes a journal of every sample it generated and every sample it declared dropped. Reconciliation passes if, after the system has drained:

1. Every journalled sample not declared dropped appears **exactly once** in `readings FINAL` (or `events FINAL`) with the expected value, stream and sensor instance.
2. For every sample on the timeline `ts` is exactly what the device sent. Samples of never-synced boots are in `readings_unanchored`, each exactly once.
3. No row exists that is not in the journal.
4. Every unexplained-loss gap corresponds to a sample the fault injector actually destroyed in a way the device could not know about; there are no others.
5. Rollups recomputed from scratch equal the stored rollups.

The queries are in `test/reconcile/` (Phase 0 build).
