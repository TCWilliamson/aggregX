-- 0001_core.sql: identity, tenancy, registry and streams (specs 03, 04, 05).
-- Expand-then-contract migrations only. Never edit a migration once merged.

BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ---------------------------------------------------------------- orgs
CREATE TABLE orgs (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id     bigint REFERENCES orgs(id),
    name          text NOT NULL,
    cell          text NOT NULL DEFAULT 'uk1',
    created_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

-- Closure table: one row per (ancestor, descendant) pair, including (x, x).
CREATE TABLE org_closure (
    ancestor_id   bigint NOT NULL REFERENCES orgs(id),
    descendant_id bigint NOT NULL REFERENCES orgs(id),
    depth         int    NOT NULL CHECK (depth >= 0),
    PRIMARY KEY (ancestor_id, descendant_id)
);
CREATE INDEX org_closure_desc ON org_closure (descendant_id);

-- ---------------------------------------------------------------- users
-- Users live in the identity provider; we keep the subject and memberships.
CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idp_subject   text NOT NULL UNIQUE,
    email         text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    org_id        bigint NOT NULL REFERENCES orgs(id),
    user_id       bigint NOT NULL REFERENCES users(id),
    role          text   NOT NULL CHECK (role IN ('owner','admin','operator','viewer','support')),
    data_classes  text[] NOT NULL DEFAULT ARRAY['diagnostic','telemetry'],
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

-- ---------------------------------------------------------------- registry
CREATE TABLE schemas (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          text NOT NULL,
    major         int  NOT NULL,
    minor         int  NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('product','module','gateway','sys')),
    status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','deprecated')),
    doc           jsonb NOT NULL,          -- parsed schema document
    source_yaml   text  NOT NULL,
    source_sha256 bytea NOT NULL,
    published_at  timestamptz,
    UNIQUE (name, major, minor)
);

CREATE TABLE retention_classes (
    key            text PRIMARY KEY,       -- e.g. standard, event, long
    hot_raw_days   int  NOT NULL,
    late_window_days int NOT NULL,
    CHECK (late_window_days <= hot_raw_days)   -- spec 06 §8: late data never outlives hot raw
);
-- hot_raw_days for 'event' is nominal: ClickHouse keeps event-class raw data indefinitely.
INSERT INTO retention_classes VALUES ('standard', 400, 30), ('event', 36500, 30), ('long', 1825, 30);

CREATE TABLE products (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,    -- 'envsense'
    title         text NOT NULL,
    default_retention_class text NOT NULL DEFAULT 'standard' REFERENCES retention_classes(key),
    allowance_tier text NOT NULL DEFAULT 'standard',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hw_revisions (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id    bigint NOT NULL REFERENCES products(id),
    key           text NOT NULL UNIQUE,    -- 'envsense-r3'
    flash_kb      int,
    notes         text NOT NULL DEFAULT ''
);

-- Which schema names and majors a product may send (spec 04 §6).
CREATE TABLE product_schemas (
    product_id    bigint NOT NULL REFERENCES products(id),
    schema_name   text   NOT NULL,
    major         int    NOT NULL,
    PRIMARY KEY (product_id, schema_name, major)
);

-- ---------------------------------------------------------------- sites, groups
CREATE TABLE sites (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        bigint NOT NULL REFERENCES orgs(id),
    parent_id     bigint REFERENCES sites(id),
    name          text NOT NULL,
    time_zone     text NOT NULL DEFAULT 'Europe/London',
    opening_hours jsonb,                   -- Later: gym utilisation
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE groups (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        bigint NOT NULL REFERENCES orgs(id),
    name          text NOT NULL,
    kind          text NOT NULL DEFAULT 'static' CHECK (kind IN ('static','dynamic','temporary')), -- dynamic/temporary: Later
    rule          jsonb,                   -- Later: dynamic group rule
    expires_at    timestamptz,             -- Later: temporary groups
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- devices
CREATE TABLE batches (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,    -- 'B-2026-11-003'
    hw_revision_id bigint NOT NULL REFERENCES hw_revisions(id),
    fw_version    text NOT NULL,
    preassign_org_id  bigint REFERENCES orgs(id),
    preassign_site_id bigint REFERENCES sites(id),
    manufactured_on date,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    serial        text NOT NULL UNIQUE CHECK (serial ~ '^[A-Z0-9-]{1,32}$'),
    kind          text NOT NULL DEFAULT 'device' CHECK (kind IN ('device','gateway')),
    hw_revision_id bigint NOT NULL REFERENCES hw_revisions(id),
    batch_id      bigint REFERENCES batches(id),
    status        text NOT NULL DEFAULT 'registered'
                  CHECK (status IN ('registered','claimed','unclaimed','suspended','revoked')),
    org_id        bigint REFERENCES orgs(id),      -- current owner; NULL unless claimed
    product_id    bigint REFERENCES products(id),
    cred_version  int    NOT NULL DEFAULT 1,       -- spec 03 §8
    claim_code_hash bytea,                          -- SHA-256 of the label's claim code
    owner_org_id  bigint REFERENCES orgs(id),      -- Later: ownership vs assignment (leases)
    last_mailbox_id bigint NOT NULL DEFAULT 0,     -- highest mailbox item id issued (spec 01 §7.2)
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'claimed') = (org_id IS NOT NULL))
);
CREATE INDEX devices_org ON devices (org_id) WHERE org_id IS NOT NULL;

CREATE TABLE device_keys (
    device_id     bigint NOT NULL REFERENCES devices(id),
    kid           bytea  NOT NULL CHECK (length(kid) = 4),
    alg           text   NOT NULL DEFAULT 'ES256' CHECK (alg IN ('ES256','EdDSA')),
    public_key    bytea  NOT NULL,
    status        text   NOT NULL DEFAULT 'active' CHECK (status IN ('active','retiring','revoked')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, kid)
);

CREATE TABLE gateway_certs (
    device_id     bigint NOT NULL REFERENCES devices(id),   -- kind = 'gateway'
    serial_number text   NOT NULL,                          -- X.509 serial
    not_before    timestamptz NOT NULL,
    not_after     timestamptz NOT NULL,
    fingerprint   bytea  NOT NULL UNIQUE,
    revoked_at    timestamptz,
    PRIMARY KEY (device_id, serial_number)
);

-- Incarnations in the order the platform first saw them (spec 05 §5).
CREATE TABLE device_incarnations (
    device_id     bigint NOT NULL REFERENCES devices(id),
    inc           bigint NOT NULL CHECK (inc > 0 AND inc < 4294967296),
    ordinal       int    NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, inc),
    UNIQUE (device_id, ordinal)
);

-- Uptime anchors, first write wins, never updated (spec 01 §8, spec 06 §4.3).
CREATE TABLE boot_anchors (
    device_id     bigint NOT NULL REFERENCES devices(id),
    inc           bigint NOT NULL,
    boot          bigint NOT NULL,
    anchor_ms     bigint NOT NULL,         -- Unix ms of uptime 0
    source        text   NOT NULL CHECK (source IN ('time_sync','receive_estimate')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, inc, boot)
);

CREATE TABLE claim_tokens (                -- order tokens (spec 03 §3.4)
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash    bytea  NOT NULL UNIQUE,
    org_id        bigint NOT NULL REFERENCES orgs(id),
    site_id       bigint REFERENCES sites(id),
    batch_id      bigint REFERENCES batches(id),
    serials       text[],
    max_uses      int,
    uses          int    NOT NULL DEFAULT 0,
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_by    bigint REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- modules, sensor instances
CREATE TABLE modules (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    serial        text NOT NULL UNIQUE,    -- 'M-7781', or the device serial for the main board
    module_type   text NOT NULL,           -- schema name, e.g. 'co2'
    registered    boolean NOT NULL DEFAULT true,  -- false = first seen in data, not at manufacture
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sensor_instances (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    module_id     bigint NOT NULL REFERENCES modules(id),
    channel_key   text   NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (module_id, channel_key)        -- upsert target: same id on every replay
);

-- ---------------------------------------------------------------- streams (spec 05)
CREATE TABLE streams (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id     bigint NOT NULL REFERENCES devices(id),
    org_id        bigint NOT NULL REFERENCES orgs(id),
    -- Positions: {"inc_ordinal": n, "seq": {"0": 6230, "1": 302, ...}}. NULL start = from the beginning.
    start_pos     jsonb,
    end_pos       jsonb,                   -- NULL = open
    opened_at     timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz,
    open_reason   text NOT NULL CHECK (open_reason IN ('claim','isolated_move','reclaim')),
    close_reason  text CHECK (close_reason IN ('isolated_move','unclaim','rma')),
    CHECK ((end_pos IS NULL) = (closed_at IS NULL))
);
CREATE UNIQUE INDEX streams_one_open ON streams (device_id) WHERE end_pos IS NULL;
CREATE INDEX streams_org ON streams (org_id);

-- Owner history: a with-data move updates streams.org_id and records it here.
CREATE TABLE stream_owners (
    stream_id     bigint NOT NULL REFERENCES streams(id),
    org_id        bigint NOT NULL REFERENCES orgs(id),
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (stream_id WITH =, valid WITH &&)
);

-- Effective-dated retention class (spec 06 §4.2). Stamped on each row by sample time.
CREATE TABLE stream_retention (
    stream_id     bigint NOT NULL REFERENCES streams(id),
    retention_class text NOT NULL REFERENCES retention_classes(key),
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (stream_id WITH =, valid WITH &&)
);

-- Schema versions each stream has reported, and when (spec 04 §3).
CREATE TABLE stream_schemas (
    stream_id     bigint NOT NULL REFERENCES streams(id),
    schema_name   text NOT NULL,
    major         int  NOT NULL,
    minor         int  NOT NULL,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    PRIMARY KEY (stream_id, schema_name, major, minor)
);

-- ---------------------------------------------------------------- memberships
CREATE TABLE group_memberships (
    group_id      bigint NOT NULL REFERENCES groups(id),
    device_id     bigint NOT NULL REFERENCES devices(id),
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (group_id WITH =, device_id WITH =, valid WITH &&)
);

CREATE TABLE site_memberships (            -- a device is at one site at a time
    site_id       bigint NOT NULL REFERENCES sites(id),
    device_id     bigint NOT NULL REFERENCES devices(id),
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (device_id WITH =, valid WITH &&)
);

-- Gateway a device was last heard through (portal view; not used for routing).
CREATE TABLE device_gateway_seen (
    device_id     bigint PRIMARY KEY REFERENCES devices(id),
    gateway_id    bigint NOT NULL REFERENCES devices(id),
    last_seen_at  timestamptz NOT NULL,
    rssi          int,
    lqi           int
);

-- ---------------------------------------------------------------- assets (core from the start; used in Phase 4)
CREATE TABLE assets (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        bigint NOT NULL REFERENCES orgs(id),
    asset_type    text NOT NULL,
    name          text NOT NULL,
    model         text,
    serial        text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE asset_mountings (
    asset_id      bigint NOT NULL REFERENCES assets(id),
    device_id     bigint NOT NULL REFERENCES devices(id),
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (device_id WITH =, valid WITH &&)
);
CREATE TABLE asset_locations (
    asset_id      bigint NOT NULL REFERENCES assets(id),
    site_id       bigint NOT NULL REFERENCES sites(id),
    zone          text,
    valid         tstzrange NOT NULL,
    EXCLUDE USING gist (asset_id WITH =, valid WITH &&)
);

-- ---------------------------------------------------------------- moves and mailbox
CREATE TABLE moves (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id     bigint NOT NULL REFERENCES devices(id),
    kind          text NOT NULL CHECK (kind IN ('with_data','isolated','unclaim','rma')),
    from_org_id   bigint REFERENCES orgs(id),
    to_org_id     bigint REFERENCES orgs(id),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','cancelled','failed')),
    mailbox_item_id bigint,
    boundary      jsonb,                   -- as reported by the device
    requested_by  bigint REFERENCES users(id),
    requested_at  timestamptz NOT NULL DEFAULT now(),
    applied_at    timestamptz
);
CREATE UNIQUE INDEX moves_one_pending ON moves (device_id) WHERE status = 'pending';

CREATE TABLE mailbox_items (
    device_id     bigint NOT NULL REFERENCES devices(id),
    id            bigint NOT NULL,          -- per-device, strictly increasing (devices.last_mailbox_id)
    type          smallint NOT NULL,        -- spec 01 §7.2
    body          bytea NOT NULL,           -- CBOR body
    prio          smallint NOT NULL DEFAULT 0,
    expires_at    timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','delivered','received','applied','failed','rejected','deferred','expired','cancelled','unknown')),
    result_detail jsonb,
    created_by    bigint REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    delivered_at  timestamptz,
    resolved_at   timestamptz,
    PRIMARY KEY (device_id, id)
);
CREATE INDEX mailbox_pending ON mailbox_items (device_id, id) WHERE status IN ('queued','delivered','received','deferred');

-- ---------------------------------------------------------------- audit
CREATE TABLE audit_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    actor_kind    text NOT NULL CHECK (actor_kind IN ('user','api_key','system','staff')),
    actor_id      bigint,
    org_id        bigint,
    action        text NOT NULL,           -- 'device.claim', 'stream.move', ...
    target_kind   text NOT NULL,
    target_id     bigint,
    detail        jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_org_at ON audit_log (org_id, at);
REVOKE UPDATE, DELETE ON audit_log FROM PUBLIC;

COMMIT;
