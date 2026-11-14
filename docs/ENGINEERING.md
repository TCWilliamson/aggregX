# Engineering standards

These rules are strict on purpose. Most of this platform is written by AI agents and reviewed by one person; the failure mode is code that looks finished, passes its own tests, and is wrong under failure. Every rule below exists to make that hard. A change that breaks a rule is not merged, however good it looks.

## 1. Test first, always

1. **Write the failing test before the code.** A pull request that adds behaviour must show the test failing without the change (state it in the PR, with the failing output) and passing with it.
2. **Tests come from the spec, not from the code.** Each test names the spec section it checks (`// spec 06 §3.1 step 2`). If the spec does not say what should happen, fix the spec first, in its own PR.
3. **Bug fixes start with a test that reproduces the bug.**
4. **No test, no merge.** Exceptions: pure documentation, generated files.
5. **Never weaken a test to make it pass.** Changing an assertion, a golden vector or a tolerance is a contract change: it needs its own PR explaining why the old expectation was wrong.
6. **Never skip, disable or quarantine a failing test** to get green. Fix it or revert the change that broke it.

## 2. What must be tested

| Area | Required tests |
|---|---|
| Wire formats (`internal/wire`, C codec) | Golden vectors for every valid and invalid case in spec 07 §2; round-trip; fuzz targets for every decoder |
| Ingest | Contract tests for every row of spec 07 §3 against real Redis and Redpanda (Compose), not mocks |
| Workers, storage | The exactly-once invariants I1–I8 (spec 06 §1), each with a test that injects the failure it guards against |
| Tenancy | Cross-tenant leakage tests for every endpoint (spec 07 §5, A7) |
| Anything with retries, ordering or time | Tests with duplicates, reordering and clock skew, not just the happy path |
| End to end | The simulator reconciliation (spec 06 §11) on every merge to main |

- **Integration over mocks.** Mock only what is outside our control (time, randomness, the network edge). Never mock Redis, Redpanda, PostgreSQL or ClickHouse in a test that claims to check correctness; use the Compose services.
- **Deterministic tests.** Inject clocks and random sources; seed everything; no `time.Sleep` to wait for a condition (poll with a deadline).
- **Coverage:** `internal/wire` and every package on the data path (ingest, workers, routing, migrations) ≥ 90% line coverage, enforced in CI. Coverage is a floor, not the goal: an untested failure path fails review even at 100%.
- **Race detector on**: `go test -race ./...` in CI.

## 3. Invariants are code

The invariants in the specs (I1–I8 in spec 06, I5's key-purity table, the routing rules in spec 05 §3.3) are enforced by:

- a named test per invariant (`TestInvariant_I3_SkipPublishOnlyWhenKnown`), and
- assertions in the code where a violation would be silent (log at error level and fail the operation; never "best effort" past a broken invariant).

Adding a ClickHouse key column, a Redis state field or a reply field without updating the spec and the invariant tests is a review failure.

## 4. Review gates

Three classes of change need **tom's review** before merge, whatever the tests say:

1. **Identity and crypto**: keys, signatures, certificates, tokens, claiming, the directory, revocation.
2. **Data loss and duplication**: acks, de-duplication, offsets, routing, retention, migrations that touch telemetry.
3. **OTA and device-side safety**: bootloader, image signing, rollback, anything that writes eFuses or locks a part.

Everything else may merge on green CI plus one independent agent review (a different model or a fresh session that did not write the code, given the diff and the spec, not the author's reasoning).

## 5. Go rules

- Go 1.26. `gofmt`, `go vet` and `golangci-lint` (config in `.golangci.yml`) must be clean. No `//nolint` without a comment saying why, reviewed like code.
- **Errors**: never ignore one. Wrap with context (`fmt.Errorf("…: %w", err)`). Errors that cross a boundary carry a stable code (see `wire.Error`). No `panic` outside `init` and truly impossible states.
- **Context** on every blocking call; every goroutine has an owner that stops it.
- **No global mutable state** except metrics registries.
- **Bounded everything**: every queue, buffer, map keyed by external input, read and request has a limit, taken from the spec's limits table.
- **Untrusted input** (anything from a device, gateway or user) is parsed with strict decoders, size-limited before parsing, and never logged as fact before verification (spec 02 §3.1).
- **Time** comes from an injected clock. **Randomness** for security from `crypto/rand` only.
- **Dependencies**: only the libraries agreed in the design (franz-go, clickhouse-go, pgx, go-redis, fxamacker/cbor, yaml.v3) plus the standard library. A new dependency needs a PR of its own that says why.
- **Packages** follow the run modes and specs; no `util` or `common` packages.
- Functions over 60 lines or with more than 4 levels of nesting need a reason in review.

## 6. C and firmware rules

- C11, `-Wall -Wextra -Werror`, plus `-fanalyzer` or clang-tidy in CI.
- No dynamic allocation after init. No recursion. Every buffer has an explicit size; every loop a bound.
- Wire encoding and decoding only through the zcbor-generated code or zcbor's streaming API (spec 04 §8.4); no hand-rolled CBOR.
- Every function that can fail returns an error code that the caller checks.
- Host-side unit tests for all logic that does not touch hardware (counters, outbox, mailbox, ack handling), run in CI against the same golden vectors as Go.
- Anything irreversible on hardware (eFuses, APPROTECT, secure boot enable) lives in the factory jig code only, behind an explicit confirmation, and is rehearsed on sacrificial units.

## 7. Specs and contracts

- The specs in `docs/specs/` and the machine-readable sources (`spec/wire/envelope.cddl`, `spec/schemas/`, `migrations/`) are the contract. Code follows them; when they disagree, the code is wrong until a spec PR says otherwise.
- A contract change (wire format, golden vectors, tables, invariants) is its own PR, reviewed before any code that depends on it.
- Migrations are expand-then-contract and never edited after merge.

## 8. Pull requests

- Small: one concern per PR, ideally under 400 changed lines excluding generated files and vectors.
- The description states: what spec sections it implements, the test that failed first, and which review gate (§4) applies.
- CI must be green: format, vet, lint, `go test -race`, coverage gates, golden vectors, CDDL validation, C vectors, and (on main) the Compose contract tests and reconciliation.
- Commits are signed off by whoever drove the work. No force-push to `main`.

## 9. Definition of done

A change is done when: the spec says what it does; a test proved it failed before and passes after; the failure cases from spec 07 that touch it are covered; CI is green; the review gate is satisfied; and the README or runbook is updated if behaviour an operator sees has changed.
