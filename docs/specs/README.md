# Platform specifications (Phase 0)

Status: **Draft 1**, 14 Nov 2026. Written against the design doc *IoT Cloud Platform: Architecture & Scaling Design*, revision 44, and the independent review of revision 33.

**One multi-tenant system** (confirmed 14 Nov): one platform serves every customer, product and device type at once, with tenancy enforced by the platform (spec 05), not one deployment per customer.

These specs turn the design doc into contracts that code, firmware and tests can be written against. Where the design doc is ambiguous, wrong, or contradicts itself, the spec says so in a **Design note** box and the issue is logged in [CONTRADICTIONS.md](CONTRADICTIONS.md) with a proposed resolution. Nothing has been resolved silently.

## Documents

| # | Spec | What it fixes |
|---|------|---------------|
| 01 | [Envelope and reply](01-envelope-and-reply.md) | CBOR wire format of every upload and every reply: fields, integer keys, timestamps, sequence series, acks, mailbox items, size limits, HTTP status codes, worked examples |
| 02 | [Batch signing and gateway relay](02-batch-signing-and-gateway.md) | COSE_Sign1 profile for device batches and cloud replies, the gateway relay protocol (HTTPS side and CoAP side), the gateway trust model and its residual risks |
| 03 | [Identity, claiming and revocation](03-identity-claiming-tokens.md) | Factory keys, registration, claim codes, direct devices (signed, no tokens), gateway certificates, revocation, CAs, signing-key hierarchy |
| 04 | [Schema registry and codegen](04-schema-registry-and-codegen.md) | Registry file format, channels, config, events, slots and sub-modules, composition epochs, versioning rules, Go and C code generation |
| 05 | [Tenancy and data streams](05-tenancy-and-streams.md) | Entities, data streams, sample routing, visibility windows, moves including `move_boundary`, PostgreSQL tables |
| 06 | [Telemetry storage and exactly-once](06-telemetry-storage-and-exactly-once.md) | Redpanda topics, ClickHouse tables, rollups, the end-to-end exactly-once contract and its invariants, reconciliation |
| 07 | [Contract and fault test plan](07-contract-test-plan.md) | Golden vectors, ingest contract tests, simulator fault scenarios, the reconciliation oracle, Phase 0 exit test |
| 08 | [State machines and process flows](08-state-machines-and-flows.md) | Format for every state machine and flow, inventory of all of them, device runtime machine as the worked example (Draft 0) |
| — | [CONTRADICTIONS.md](CONTRADICTIONS.md) | Register of design-doc issues found while writing these specs, with proposed resolutions and who decides |

Machine-readable sources live in the repository, not in these documents:

- `spec/wire/envelope.cddl`: the normative wire grammar (spec 01 and 02).
- `spec/schemas/*.yaml`: example product and module schemas (spec 04).
- `migrations/postgres/*.sql`, `migrations/clickhouse/*.sql`: the tables (specs 05 and 06).
- `spec/vectors/`: golden test vectors (spec 07).

If a document and a machine-readable source disagree, the machine-readable source wins and the document has a bug.

## Conventions

- **MUST, MUST NOT, SHOULD, MAY** are used as in RFC 2119.
- **Pilot** means built for the first deployment. **Later** means the hook (field, column, enum value) exists now and the behaviour is built in a later phase. Every Later item names its hook.
- Times on the wire are **unsigned integer milliseconds**. Wall-clock times are milliseconds since the Unix epoch, UTC. Durations are milliseconds unless the field name ends in `_s`.
- CBOR means RFC 8949 with **deterministic encoding** (§4.2.1: shortest-form integers and lengths, definite lengths, map keys sorted bytewise). Decoders MUST accept any valid encoding; encoders in the SDK and simulator MUST produce deterministic encoding so golden vectors are byte-exact.
- Examples are shown in CBOR diagnostic notation (RFC 8949 §8) with a JSON-style comment naming each integer key. Hex dumps in the examples were produced by the Go reference encoder in `internal/wire` and are checked by its tests.
- Identifiers: internal ids are PostgreSQL `bigint`. Public APIs expose them as opaque prefixed strings (`dev_…`, `st_…`, `org_…`). Device and gateway **serials** are the printed, human-facing identifiers and are what devices put on the wire.

## Glossary

| Term | Meaning |
|------|---------|
| Device | A physical unit with a factory key. Permanent identity: its serial. |
| Gateway | A Thread border router that we make and run. Relays signed device batches over mutually authenticated HTTPS. |
| Direct device | A device that talks HTTPS to ingest itself (WiFi). Signs every batch with its factory key, like a Thread device (spec 03 §4). |
| Envelope | One upload's logical content (spec 01). |
| Batch | One envelope as sent on the wire, signed (COSE_Sign1) or not. |
| Series | An independent sequence-number counter on a device: main records, events, high-rate blocks, and one per module slot. |
| Incarnation (`inc`) | A random 32-bit id a device regenerates whenever it loses its sequence counters (factory reset, flash wipe). Keeps old and new sequence numbers apart. |
| Sample | One record, one event, or one high-rate block: the unit that a sequence number counts. |
| Data stream | This device, owned by this org, for this period. Telemetry hangs off the stream, not the org. |
| Sensor instance | A physical sensor: module serial plus channel key. Calibrations attach here. |
| Composition epoch (`comp`) | Counter the device bumps whenever a module is added, removed or swapped. |
| Mailbox item | A server-to-device instruction delivered on a reply: config, command, OTA offer, move boundary, etc. |
| Run modes | `ingest`, `core`, `workers`: three roles of one Go binary. |
