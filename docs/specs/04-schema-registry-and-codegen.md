# 04. Schema registry and code generation

Status: Draft 1. Example schemas: `spec/schemas/`. Generator: `cmd/iotgen`.

The schema registry describes every product and sub-module: its channels (telemetry), config (the twin's shape), commands and events. One registry drives payload decoding, validation, storage, rollups, access control (data classes), calibration, the twin and the API. Storage itself is schema-agnostic: no table changes when a schema is added.

## 1. Schema kinds and identifiers

| Kind | Example | Has slots | Used in envelope |
|---|---|---|---|
| `product` | `envsense@3.2` | yes | `schema` (key 4), main records, events, blocks with slot 0 |
| `module` | `co2@2.0` | no | `mod[slot]` sections |
| `gateway` | `gw@1.0` | no | the gateway's own envelope |
| `sys` | `sys@1.0` | no | built in; channels ≥ 32768 valid in every envelope |

An identifier is `name@major.minor`. `name` is `[a-z][a-z0-9_]{1,31}`. A device always sends the exact version its firmware was built against.

## 2. File format

Schemas are authored as YAML in the repository (`spec/schemas/<name>.yaml`), reviewed like code, and published to the registry by CI (§6). Published versions are immutable.

```yaml
schema: envsense
kind: product
version: 3.2
title: Environmental sensor
channels:
  - id: 1
    key: temp_c
    type: float
    unit_in: degC           # what the device sends
    unit_out: degC          # what users see after calibration
    range: [-40, 125]       # valid raw range; outside = flagged, not rejected
    precision: 0.01
    agg: [avg, min, max]    # rollup statistics (spec 06 §7)
    class: telemetry        # diagnostic | telemetry | location | sensitive
    calibration: [linear, polynomial]
  - id: 2
    key: rh_pct
    type: float
    unit_in: pct
    range: [0, 100]
    agg: [avg, min, max]
    class: telemetry
  - id: 3
    key: door
    type: enum
    values: {0: closed, 1: open}
    agg: [time_in_state, last]
    class: telemetry
  - id: 4
    key: pulses
    type: int
    agg: [sum]
    class: telemetry
    counter: true           # monotonic counter; rollups use deltas (Later)
slots:
  - slot: 1
    key: port1
    accepts: [co2@2, pm25@1]   # module schema names and majors allowed here
config:
  - id: 1
    key: sample_s
    type: int
    range: [1, 86400]
    default: 60
  - id: 2
    key: report_s
    type: int
    range: [10, 86400]
    default: 300
  - id: 3
    key: burst_threshold
    type: float
    default: 2.5
commands:
  - id: 1
    key: reboot
  - id: 2
    key: identify
    args:
      - {id: 1, key: seconds, type: int, range: [1, 300], default: 10}
events:
  - id: 1000
    key: tamper
    attrs:
      - {id: 1, key: severity, type: int}
calibration:
  device_calibrated: []     # channel keys calibrated on the device itself
```

### 2.1 Channel fields

| Field | Required | Meaning |
|---|:-:|---|
| `id` | ✓ | uint16. 1–32767 in product, module and gateway schemas; 32768–65535 reserved for `sys`. |
| `key` | ✓ | `[a-z][a-z0-9_]{0,31}`. Used in APIs as `key` (main board) or `slotkey.key` (`port1.co2_ppm`). |
| `type` | ✓ | `float`, `int`, `bool`, `enum`, `string`, `geo` |
| `unit_in`, `unit_out` | | UCUM-style unit codes. `unit_out` defaults to `unit_in`. |
| `range` | | Valid raw range. Values outside are stored with the `out_of_range` quality flag. |
| `precision` | | Display and export rounding. Never applied to stored values. |
| `values` | enum | Map of uint → label. |
| `agg` | ✓ | Subset of `avg`, `min`, `max`, `sum`, `last`, `time_in_state`. Decides what the rollup tables mean for this channel. |
| `class` | ✓ | Data class for access control. |
| `calibration` | | Allowed methods. Pilot: `linear`, `polynomial`. Absent = not calibratable. |
| `counter` | | Monotonic counter (rollup by delta, reset detection). Later; the flag is the hook. |
| `deprecated` | | The channel is retired (§3). Its id is never reused. |

### 2.2 Type mapping

| Schema type | On the wire | In ClickHouse `readings` (spec 06) |
|---|---|---|
| `float` | CBOR float or int | `v` Float64 |
| `int` | CBOR int (64-bit) | `v` Float64 (exact up to 2⁵³; schemas MUST NOT declare a wider range) |
| `bool` | CBOR true/false | `v` 0 or 1 |
| `enum` | uint | `v` |
| `string` | tstr ≤ 256 bytes | `vs` String |
| `geo` | `[lat, lon]` or `[lat, lon, alt, acc]` | `v` = lat, `v2` = lon, `v3` = alt, `v4` = acc |

## 3. Versioning rules

1. **Ids are permanent within a schema name, across all versions.** Channel 3 of `envsense` means `door` forever. A removed channel is marked `deprecated` and its id is retired, never reused. The same holds for config field, command, event and attribute ids.
2. **Minor** version: adds channels, config fields, commands, events, attributes, slots or accepted modules; widens a range; adds an enum value; changes titles or units of display (`unit_out`, `precision`).
3. **Major** version: changes a channel's `type` or `unit_in`, narrows a range, removes an enum value, changes `agg`, removes a slot. Because of rule 1, removing a channel is a minor change (deprecation), so majors should be rare.
4. Versions coexist. A device on `envsense@3.1` keeps working after `3.2` is published. Each stream records which schema versions it has reported and when (spec 05).
5. **Release order:** a schema version is published to the registry, in staging first, before any firmware that sends it is released. CI refuses to build a firmware release whose schema version is not published (§6).

> **Design note.** The design doc says removing a channel is a major version. With permanent ids, a removal can safely be a deprecation in a minor version, and major versions are reserved for changes that alter the meaning of an existing id. This is a refinement, not a contradiction.

## 4. Config

### 4.1 Fields and addressing

- Config fields have the same shape as channels (`id`, `key`, `type`, `range`, `default`, `values`).
- On the wire (the `cfg` mailbox item and `rcfg`) a field is addressed by `slot × 65536 + id`, slot 0 being the main board. A module's config fields come from its module schema.
### 4.2 Inheritance

- **Effective desired config** = schema defaults, then product overrides, then org, then group, then device (spec of the twin, Phase 1).

### 4.3 Config hash

The `cfg` header (spec 01 key 7) is the first 8 bytes of SHA-256 over the **canonical config**: a CBOR map from address (`slot × 65536 + id`) to value, containing every field the device implements including defaults, deterministically encoded. The SDK and the platform compute it the same way; golden vectors cover it (spec 07).

## 5. Slots, modules and sensor instances

- A product declares **slots**. Each slot accepts a list of module schema names and majors.
- The device reports its **composition** (module type, serial and schema in each slot) in a `composition` event (spec 01 §4.5) whenever it changes, and increments `comp`.
- Every module section in an envelope carries the module's serial and schema, so each record is attributed by the data in its own section, not by the latest composition. `comp` is a consistency check and lets the platform detect a composition change it missed.
- **Sensor instance** = (module serial, channel key). For the main board the module serial is the device serial. Workers resolve each record to a `sensor_instance_id` (spec 05 §2) and store it on every row. Calibrations attach to sensor instances, so a module moved to another device or slot carries its calibration with it.
- Modules are registered at manufacture (`M-…` serials). A module serial seen for the first time that is not registered is still stored (and a sensor instance created), with an alert, so a registration slip never loses data.

## 6. The registry service

- Published schemas are stored in PostgreSQL (`schemas`, spec 05 §8) as the parsed document plus the source YAML and its SHA-256. Status: `draft`, `published`, `deprecated`.
- A product lists which schema names and majors its hardware revisions may send (`product_schemas`).
- Workers load all published schemas at start and refresh on change notification. Ingest never needs them.
- CI publishes with `iotgen publish --registry <url> spec/schemas/*.yaml`, which validates every file, checks the versioning rules of §3 against the previously published version, and refuses on violation.

## 7. Validation and quarantine (workers)

| Problem | Outcome |
|---|---|
| Envelope or section schema not published | Whole envelope to quarantine |
| Module schema not accepted in that slot by the product | Section to quarantine; rest stored |
| Unknown channel id in a known schema | Record to quarantine; other records stored |
| Value of the wrong CBOR type for the channel | Record to quarantine |
| Value outside `range` | **Stored**, flagged `out_of_range` |
| Timestamp outside the late-data window or > 5 min in the future | Envelope to quarantine |
| Unknown event type or attribute | Event to quarantine |

Quarantined data keeps its raw bytes and source offset and can be replayed once the schema is fixed (spec 06 §5).

> **Design note (C12).** The design doc quarantines "anything that fails". This spec stores out-of-range values with a flag instead: a sensor reading 130 °C is a fault worth seeing in the data, not a decoding problem.

## 8. Code generation

### 8.1 What is generated, and from what

| Source | Generator | Output | Used by |
|---|---|---|---|
| `spec/wire/envelope.cddl` (envelope, reply, relay, COSE profile) | **zcbor** (Nordic's CDDL code generator, shipped with nRF Connect SDK; plain C, also builds under ESP-IDF) | C encode/decode functions and structs | Device SDK, gateway firmware |
| `spec/wire/envelope.cddl` | none: hand-written `internal/wire` | Go types and codec | Ingest, workers, simulator |
| `spec/schemas/*.yaml` | `cmd/iotgen` | C header per schema version: channel, config, command, event and slot ids, enum values, schema string, config canonical-hash helper | Device firmware |
| `spec/schemas/*.yaml` | `cmd/iotgen` | Go package with the same constants, plus an embedded registry for tests and the simulator | Simulator, tests |

### 8.2 Keeping Go and C on the same contract

There is no maintained CDDL-to-Go generator, so the Go wire codec is written by hand. Three checks keep it from drifting:

1. **Golden vectors** in `spec/vectors/`: CBOR bytes plus their expected decoded form, valid and invalid. The Go codec, the C codec and the simulator all run the same vectors (spec 07 §2).
2. **CDDL validation in CI**: every vector and a corpus of Go-encoded random envelopes is validated against `envelope.cddl` with zcbor's validator.
3. **Round-trip fuzzing** of the Go codec.

> **Design note (C16).** The design doc and Phase 0 plan say Go and C are both generated from one spec file. This spec makes the CDDL the single source, generates C from it, and checks the hand-written Go against it mechanically. The alternative, a home-made YAML format with our own generator for both languages, means writing and maintaining a C CBOR generator, which zcbor already is. Decision for tom; see CONTRADICTIONS.md C16.

### 8.3 Generated C header (example)

```c
/* Generated by iotgen from spec/schemas/envsense.yaml. Do not edit. */
#define ENVSENSE_SCHEMA            "envsense@3.2"
#define ENVSENSE_CH_TEMP_C         1u   /* float degC  telemetry */
#define ENVSENSE_CH_RH_PCT         2u
#define ENVSENSE_CH_DOOR           3u
#define ENVSENSE_DOOR_CLOSED       0u
#define ENVSENSE_DOOR_OPEN         1u
#define ENVSENSE_SLOT_PORT1        1u
#define ENVSENSE_CFG_SAMPLE_S      ((0u << 16) | 1u)
#define ENVSENSE_CFG_SAMPLE_S_DEFAULT 60
#define ENVSENSE_CMD_REBOOT        1u
#define ENVSENSE_EV_TAMPER         1000u
```

### 8.4 What the first zcbor run showed

The CDDL was run through zcbor 0.9 (`scripts/c-vectors.sh`), the generated C compiled, and every golden vector decoded by it. Three findings shape the device SDK:

1. **Struct size.** zcbor generates fixed-size structs sized by the CDDL's maximum counts. With the cloud limits (1,000 items per section) one `struct envelope` is about 82 MB; with a device profile of 32 items it is still about 670 KB, more than an nRF52840's 256 KB of RAM. The SDK therefore uses the generated **reply decoder** (small) and encodes envelopes with zcbor's streaming encode API, record by record from the outbox, not with a generated envelope struct. `scripts/c-vectors.sh` generates from a device-profile copy of the CDDL with smaller limits (`DEV_MAX_ITEMS`, `DEV_MAX_BLOCK`).
2. **Float width.** zcbor re-encodes floats at a fixed width, so C output is not byte-identical to the Go reference for float values. This is allowed (spec 01 §2): encoders must be deterministic for integers, lengths and key order; floats should be shortest. The C vector runner compares by decoded value.
3. **`any` is not preserved.** zcbor skips `any` on decode and cannot re-encode it, so the CDDL now types every field a device must produce (for example the `move_boundary` result is `{series => seq}`, not `any`).

## 9. Built-in `sys` schema

Platform channels any envelope may carry, ids from 32768:

| Id | Key | Type | Class |
|---:|---|---|---|
| 32768 | `battery_mv` | int | diagnostic |
| 32769 | `battery_pct` | int | diagnostic |
| 32770 | `power_source` | enum (0 battery, 1 mains, 2 USB, 3 solar) | diagnostic |
| 32771 | `rssi_dbm` | int | diagnostic |
| 32772 | `lqi` | int | diagnostic |
| 32773 | `uptime_s` | int | diagnostic |
| 32774 | `outbox_depth` | int | diagnostic |
| 32775 | `outbox_oldest_s` | int | diagnostic |
| 32776 | `temp_mcu_c` | float | diagnostic |
| 32777 | `heap_free` | int | diagnostic |
| 32778 | `parent_rloc16` | int | diagnostic |
