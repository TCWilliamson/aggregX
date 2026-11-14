-- 0002_pipeline.sql: worker-side pipeline state (spec 06).

BEGIN;

-- Highest sequence number seen per series, for gap detection (spec 06 §6).
CREATE TABLE series_state (
    device_id     bigint NOT NULL REFERENCES devices(id),
    inc           bigint NOT NULL,
    series        int    NOT NULL,
    high_seq      bigint NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, inc, series)
);

CREATE TABLE series_gaps (
    device_id     bigint NOT NULL REFERENCES devices(id),
    inc           bigint NOT NULL,
    series        int    NOT NULL,
    from_seq      bigint NOT NULL,
    to_seq        bigint NOT NULL,           -- inclusive
    state         text   NOT NULL CHECK (state IN ('pending','skipped','dropped','unexplained')),
    detected_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at   timestamptz,
    CHECK (to_seq >= from_seq),
    PRIMARY KEY (device_id, inc, series, from_seq)
);
CREATE INDEX series_gaps_pending ON series_gaps (detected_at) WHERE state = 'pending';

-- Rollup buckets touched by new raw data (spec 06 §7.2).
CREATE TABLE rollup_dirty (
    stream_id     bigint      NOT NULL,
    hour          timestamptz NOT NULL,
    marked_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stream_id, hour)
);
CREATE INDEX rollup_dirty_marked ON rollup_dirty (marked_at);

-- Accepted samples and bytes per stream per day (metering, design doc "Platform users").
CREATE TABLE metering_daily (
    stream_id     bigint NOT NULL,
    day           date   NOT NULL,
    samples       bigint NOT NULL DEFAULT 0,
    events        bigint NOT NULL DEFAULT 0,
    bytes         bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (stream_id, day)
);
-- NOTE: metering counts must be idempotent under replay. Workers record the
-- (partition, offset) high-water they have counted in metering_offsets and skip
-- anything at or below it.
CREATE TABLE metering_offsets (
    topic         text NOT NULL,
    partition     int  NOT NULL,
    counted_upto  bigint NOT NULL,
    PRIMARY KEY (topic, partition)
);

COMMIT;
