# 05. Tenancy and data streams

Status: Draft 1. Tables: `migrations/postgres/0001_core.sql`.

## 1. The idea in one paragraph

Telemetry is never keyed by org. Every stored row carries a **data stream** id, and a stream means "this device, owned by this org, for this period". Which org can see a row is a fact about its stream, kept in PostgreSQL. So moving a device between groups or sites changes membership rows; moving it to another org with its history changes one column on the stream; and moving it with complete isolation closes one stream and opens another at an exact point in the device's sequence numbers. No telemetry is ever rewritten.

## 2. Entities (pilot)

| Entity | Table | Notes |
|---|---|---|
| Org | `orgs`, `org_closure` | Tree of any depth. Closure table makes "everything under X" one indexed lookup. Pilot uses two levels (us and the customer). |
| User | `users`, `org_members` | Subject from the identity provider; role and data classes per org membership. |
| Product | `products`, `hw_revisions`, `product_schemas` | A device type. Default retention class and allowance tier. |
| Schema | `schemas` | Spec 04. |
| Device / gateway | `devices`, `device_keys`, `gateway_certs` | `kind` distinguishes them; gateways are devices that also relay. Permanent. |
| Incarnation | `device_incarnations` | Each `inc` a device has used, numbered in the order first seen. |
| Module | `modules` | Physical module, by serial. The main board is a module whose serial is the device serial. |
| Sensor instance | `sensor_instances` | (module, channel key). Calibrations attach here. |
| Data stream | `streams`, `stream_owners`, `stream_retention`, `stream_schemas` | §3. |
| Site | `sites`, `site_memberships` | Has a time zone. A device is at one site at a time. |
| Group | `groups`, `group_memberships` | Static in the pilot. A device can be in many groups. |
| Asset | `assets`, `asset_mountings`, `asset_locations` | The thing a device is fitted to (a shipper, a machine, a patient episode, an apartment, a gauge). **Brought forward to Phase 1 (decided 14 Nov, C53)**: customer scenarios show the unit customers care about is often not the device. Specified in a dedicated spec, to be written. |
| Move | `moves` | §6. |
| Mailbox item | `mailbox_items` | Spec 01 §7.2. |
| Audit entry | `audit_log` | Append-only. |

All memberships, mountings and locations are **temporal** (`valid tstzrange`) with exclusion constraints, so history is preserved and overlaps are impossible.

## 3. Data streams

A stream has a device, an owning org, and a **start** and **end position** in the device's sequence space.

### 3.1 Positions

A sample's position is `(inc_ordinal, series, seq)`, where `inc_ordinal` is the order in which the platform first saw that incarnation of the device (`device_incarnations.ordinal`).

A **boundary** is `{inc_ordinal: b, seq: {series: n, …}}`. A sample is **before** the boundary if:

- its `inc_ordinal < b`, or
- its `inc_ordinal = b` and its `seq < n[series]`, where a series missing from the boundary counts as `n = 0` (it did not exist yet, so everything in it is after).

Otherwise it is **at or after** the boundary.

A stream contains a sample if the sample is at or after `start_pos` (or `start_pos` is NULL) and before `end_pos` (or `end_pos` is NULL). For any device, the streams' ranges never overlap and leave no holes between the first claim and now.

### 3.2 Why positions and not timestamps

Timestamps can be wrong, unsynced (uptime) or arrive weeks late from an outbox. Sequence numbers are assigned at capture time, in order, by the device itself, and a `move_boundary` is applied by the device between two captures. Only positions say exactly which side of a move a sample was taken on.

### 3.3 Invariants

1. A device has at most one open stream (`streams_one_open`).
2. Closed streams never change their positions.
3. `streams.org_id` changes only by a with-data move, recorded in `stream_owners`.
4. A row's stream id is a pure function of the sample's position and the stream table, so replaying the same sample later routes it to the same stream (spec 06 §4).

## 4. Lifecycle

| Event | Stream effect |
|---|---|
| Claim (first ever) | Open stream, `start_pos = NULL` (everything this device has captured, including during installation, belongs to the first owner). |
| With-data move | `streams.org_id` updated; old `stream_owners` row closed, new one opened. |
| Isolated move | Old stream closed at the device-reported boundary; new stream opened at the same boundary under the new org (§6). |
| Unclaim / RMA | Stream closed at the device-reported boundary (same mechanism as an isolated move). Device status `unclaimed`. |
| Re-claim after unclaim | New stream opened with `start_pos` = previous stream's `end_pos`. Samples captured while the device was unclaimed go to the next owner. |

## 5. Routing a sample to a stream (workers)

For each sample a worker computes `(device_id, inc_ordinal, series, seq)`:

1. Look up `inc_ordinal`; if the incarnation is new, insert it with the next ordinal (`INSERT … ON CONFLICT DO NOTHING`, then read back: whoever inserted first wins, and every replay reads the same ordinal).
2. Find the device's stream whose range contains the position (§3.1). Workers cache each device's streams; the cache is invalidated by the move transaction.
3. If no stream contains it (the device was never claimed, which ingest should have prevented), quarantine.
4. **Time guard.** If the sample's timestamp is more than 1 hour before its stream's `opened_at` for a stream opened by a move, set the `routing_suspect` quality flag and alert. Routing still follows the position.

Workers process a device's messages in order (Redpanda key = device, spec 06 §2), and within one envelope they apply any `move_boundary` result in `mres` **before** routing that envelope's samples.

## 6. Moves

### 6.1 Between groups or sites

Close one membership row (`upper(valid) = now()`), open another. No stream or telemetry change. Group and site history views use the membership ranges.

### 6.2 To another org, with data

One transaction: update `streams.org_id`, close and open `stream_owners` rows, bump `devices.org_id` and `cred_version`, audit. The new owner sees the device's whole history in that stream; the old owner no longer sees any of it. Twin history moves too.

### 6.3 To another org, isolated

```
portal: move SN-00107 to org B, isolated
  └─ core: INSERT moves (pending), INSERT mailbox_items (move_boundary, id 31)
       └─ device: applies at its next check-in, reports boundary in mres
            └─ worker: sees mres [31, applied, {0: 6230, 1: 302, ...}]
                 └─ core transaction:
                      close old stream: end_pos = {inc_ordinal, seq: boundary}
                      open new stream under org B: start_pos = same boundary
                      moves.status = applied, devices.org_id = B, cred_version + 1
                      reset desired config to org B's defaults (new cfg item)
                      audit
```

- Until the device applies the boundary, all its samples go to the old stream: the move takes effect at the device, not in the portal. The portal shows the move as pending.
- Samples numbered below the boundary that arrive later from the outbox still go to the old stream and stay with org A.
- **SDK rule:** after applying a `move_boundary` the device MUST send the envelope carrying its `mres` before, or together with, any sample at or after the boundary, and MUST NOT send samples at or after the boundary until that envelope has been acknowledged. Per-device ordering through the gateway and Redpanda then guarantees the worker sees the boundary first.
- If a worker ever finds samples at or after a new boundary already stored in the old stream (an SDK bug), it alerts; correcting it is a manual rewrite job.
- A pending move can be cancelled until the device applies it (the mailbox item is cancelled; a device that already applied it reports the boundary anyway, and the move is then applied).
- **Forced boundary** (Later): for a device that will never check in, an admin can set the boundary from the highest accepted sequence numbers plus a time guard. Not in the pilot, because a powered-off device's unsent samples could land on the wrong side.

> **Design note (C2 in part, C4).** The design doc's description is followed, with the boundary as a per-series vector and with the ordering rule above, which the design doc does not state but the mechanism depends on.

### 6.4 Unclaim and RMA

As §6.3 with no new stream: the old stream is closed at the boundary, device status becomes `unclaimed`, org cleared, and an `unclaim` mailbox item wipes the device's config. Samples captured after the boundary are held for the next owner (§4).

## 7. Visibility

All tenant filtering happens in one place: the **query layer** in core. Every API, portal, export and live path calls it.

1. Resolve the caller (user session or API key) to the orgs where it holds the needed capability, expanded downwards through `org_closure`.
2. The visible streams are those whose `org_id` is in that set. Every stored row carries its stream id, so the filter pushed to ClickHouse is `stream_id IN (…)` (or a join against a temporary set for large orgs).
3. Group and site views intersect the device's stream ranges with the membership ranges to produce `(stream_id, from_ts, to_ts)` **windows**.
4. Data classes: channels whose class the caller lacks are removed from the query (spec 04 `class`).
5. The resolved access is cached per session for 60 s and invalidated on tree, membership or move changes.

Later, leases add assignment windows to step 2 (`devices.owner_org_id` is the hook) and upward grants filter data classes per assignment.

ClickHouse row policies MAY be added as a second line of defence; nothing relies on them. Cross-tenant leakage tests are part of the contract suite (spec 07 §5).

## 8. Table summary

See `migrations/postgres/0001_core.sql` for the definitions. Tables owned by later phases and only hooked here: twins and config versions (Phase 1), OTA artifacts and deployments (Phase 1), calibrations (Phase 2), API keys and webhooks (Phase 1), leases and grants (Phase 4).

Ingest-side state tables (`series_gaps`, `rollup_dirty`) are in spec 06 and `0002_pipeline.sql`.
