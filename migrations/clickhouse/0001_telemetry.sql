-- 0001_telemetry.sql: ClickHouse tables (spec 06).
-- Invariant (spec 06 §4.1): every column in a PARTITION BY or ORDER BY below is a pure
-- function of the raw batch, its receive time, and first-write-wins registry facts.
-- Retention TTLs mirror the retention_classes table in PostgreSQL; adding a class is a migration.

CREATE DATABASE IF NOT EXISTS iot;

-- Periodic readings, module readings, and expanded high-rate block samples.
CREATE TABLE IF NOT EXISTS iot.readings
(
    stream_id           UInt64,
    sensor_instance_id  UInt64,
    channel_id          UInt16,
    ts                  DateTime64(3, 'UTC'),   -- effective time (synced, or anchored uptime)
    inc_ordinal         UInt16,
    series              UInt16,                 -- 0 main, 2 blocks, 16+slot modules
    seq                 UInt32,
    idx                 UInt16,                 -- sample index inside a block; 0 for records
    v                   Float64,                -- numeric value; geo latitude
    vx                  Array(Float64),         -- geo: [lon] or [lon, alt, acc]; empty otherwise
    vs                  String,                 -- string channels; empty otherwise
    tb                  UInt8,                  -- 0 synced, 1 uptime
    ts_dev              Int64,                  -- t0 + dt as sent, in its timebase
    boot                UInt32,
    received_at         DateTime64(3, 'UTC'),
    quality             UInt16,                 -- bit flags, spec 06 §4.4
    retention_class     LowCardinality(String),
    src_partition       UInt16,
    src_offset          UInt64
)
ENGINE = ReplacingMergeTree
PARTITION BY (retention_class, toYYYYMMDD(ts))
ORDER BY (stream_id, sensor_instance_id, channel_id, ts, inc_ordinal, series, seq, idx)
-- 'event' class (burst sessions) has no TTL: kept at full resolution (design doc, Tiers).
TTL toDateTime(ts) + INTERVAL 400 DAY DELETE WHERE retention_class = 'standard',
    toDateTime(ts) + INTERVAL 1825 DAY DELETE WHERE retention_class = 'long'
SETTINGS ttl_only_drop_parts = 1,
         non_replicated_deduplication_window = 10000;

-- Events and sessions (series 1).
CREATE TABLE IF NOT EXISTS iot.events
(
    stream_id           UInt64,
    event_type          UInt16,
    start_ts            DateTime64(3, 'UTC'),
    inc_ordinal         UInt16,
    seq                 UInt32,
    slot                UInt8,
    dur_ms              Nullable(UInt32),
    attrs_n             Map(UInt16, Float64),
    attrs_s             Map(UInt16, String),
    tb                  UInt8,
    ts_dev              Int64,
    boot                UInt32,
    received_at         DateTime64(3, 'UTC'),
    quality             UInt16,
    retention_class     LowCardinality(String),
    src_partition       UInt16,
    src_offset          UInt64
)
ENGINE = ReplacingMergeTree
PARTITION BY (retention_class, toYYYYMMDD(start_ts))
ORDER BY (stream_id, event_type, start_ts, inc_ordinal, seq)
-- Events are kept indefinitely (design doc, Tiers: long-term). Per-contract limits are Later.
SETTINGS ttl_only_drop_parts = 1,
         non_replicated_deduplication_window = 10000;

-- High-rate block metadata (one row per block; samples are expanded into readings).
CREATE TABLE IF NOT EXISTS iot.blocks
(
    stream_id           UInt64,
    sensor_instance_id  UInt64,
    channel_id          UInt16,
    start_ts            DateTime64(6, 'UTC'),   -- exact start, microseconds
    inc_ordinal         UInt16,
    seq                 UInt32,
    hz                  Float64,
    n                   UInt16,
    received_at         DateTime64(3, 'UTC'),
    retention_class     LowCardinality(String)
)
ENGINE = ReplacingMergeTree
PARTITION BY (retention_class, toYYYYMMDD(start_ts))
ORDER BY (stream_id, sensor_instance_id, channel_id, start_ts, inc_ordinal, seq)
TTL toDateTime(start_ts) + INTERVAL 400 DAY DELETE WHERE retention_class = 'standard',
    toDateTime(start_ts) + INTERVAL 1825 DAY DELETE WHERE retention_class = 'long'
SETTINGS ttl_only_drop_parts = 1;

-- Anything workers could not store (spec 04 §7). Replayable.
CREATE TABLE IF NOT EXISTS iot.quarantine
(
    received_at         DateTime64(3, 'UTC'),
    device_serial       String,
    device_id           UInt64,
    inc                 UInt32,
    series              UInt16,
    first_seq           UInt32,
    item_count          UInt32,
    scope               LowCardinality(String),  -- envelope | section | record | event
    reason_code         LowCardinality(String),
    reason              String,
    raw                 String,                  -- CBOR bytes of the batch (COSE or plain)
    src_partition       UInt16,
    src_offset          UInt64
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(received_at)
ORDER BY (device_serial, inc, series, first_seq, scope, reason_code)
TTL toDateTime(received_at) + INTERVAL 400 DAY;

-- Rollups in raw units (spec 06 §7). Version column = computed_at; latest wins.
CREATE TABLE IF NOT EXISTS iot.rollup_1m
(
    stream_id           UInt64,
    sensor_instance_id  UInt64,
    channel_id          UInt16,
    bucket              DateTime('UTC'),
    cnt                 UInt32,
    sum                 Float64,
    min                 Float64,
    max                 Float64,
    last                Float64,
    last_ts             DateTime64(3, 'UTC'),
    computed_at         DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(computed_at)
PARTITION BY toYYYYMM(bucket)
ORDER BY (stream_id, sensor_instance_id, channel_id, bucket)
TTL bucket + INTERVAL 400 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS iot.rollup_30m AS iot.rollup_1m
ENGINE = ReplacingMergeTree(computed_at)
PARTITION BY toYYYYMM(bucket)
ORDER BY (stream_id, sensor_instance_id, channel_id, bucket)
TTL bucket + INTERVAL 1825 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS iot.rollup_1d AS iot.rollup_1m
ENGINE = ReplacingMergeTree(computed_at)
PARTITION BY toYear(bucket)
ORDER BY (stream_id, sensor_instance_id, channel_id, bucket);
