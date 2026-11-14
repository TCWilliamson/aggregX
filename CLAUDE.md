# Instructions for agents working in this repository

Read `docs/ENGINEERING.md` before writing any code. Its rules are not optional. The short version:

1. **Test first.** Write the failing test, run it, show it failing, then write the code. Tests cite the spec section they check.
2. **The specs are the contract.** `docs/specs/` and `spec/wire/envelope.cddl`, `spec/schemas/`, `migrations/`. If the spec is silent or wrong, stop and propose a spec change; do not invent behaviour in code.
3. **Never weaken, skip or delete a test to get green.** Never regenerate golden vectors (`make vectors`) unless the task is an explicit contract change.
4. **No mocks of Redis, Redpanda, PostgreSQL or ClickHouse** in correctness tests; use the Compose stack (`make up`).
5. **Do not merge** changes to identity/crypto, acks/de-duplication/offsets/routing/retention, or OTA/bootloader code. Open the PR and say it needs tom's review (ENGINEERING §4).
6. **No new dependencies** without a separate PR justifying them.
7. Before saying a task is done, run: `gofmt -l .` (empty), `go vet ./...`, `golangci-lint run`, `go test -race ./...`, `make cddl`, `make c-vectors`. Report the actual output, including failures.

Useful commands: `make test`, `make up`, `make smoke`, `make schemas`, `make cddl`, `make c-vectors`.

Key invariants you must not break: spec 06 §1 (I1–I8) and §4.1 (key purity), spec 05 §3.3 (routing), spec 01 §7.1 (a device deletes data only on a verified cloud ack).
