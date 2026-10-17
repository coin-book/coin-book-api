# coin-book-api

Go REST API of coin-book, a self-hosted personal finance app; every user sees only their own rows (ADR 208: row-level security keyed on the request's user, never a WHERE clause).
Spec: `../technicka-specifikace.md`; bank formats: `../importer-formaty.md`.

## Commands

- `make test` – unit and golden tests; `make test-integration` – acceptance tests (Docker).
- `make lint`, `make openapi` (regenerate `openapi.yaml` after changing any handler or DTO), `make openapi-check`.
- Run the stack: `make up` in `../coin-book-local`.

## Layout

- `internal/domain/<context>` – business logic; `repo`/`service` with raw pgx SQL, no ORM.
- `internal/transport/http/<context>` – Huma handlers and DTOs; `httpapi.Register` declares the access tier.
- `internal/platform` – config, db, migrate, money, clock, sensitive.
- `internal/app` – dependency wiring shared by the server and `cmd/openapi`.
- `db/migration` – append-only, numbered `NNN_name.up.sql`/`.down.sql`. Never edit a released migration.

## Rules

- Money is `int64` minor units plus an ISO currency, never float; never add across currencies.
- Calendar days are `DATE` in Europe/Prague; instants are UTC `TIMESTAMPTZ`.
- Every write operation is `httpapi.AuthSession`; agent tokens only reach `AuthRead` operations with a scope.
- Data tables carry `user_id` with a policy; a new table gets both in its migration (the acceptance test `TestEveryUserTableIsProtectedByRowSecurity` fails otherwise). Queries never filter by user themselves, except in `agentaccess`.
- `make test-integration` and `make up` need a database without pre-release transactions: migration 008 refuses them.
- Account numbers and messages go to logs only as `sensitive.AccountNumber` / `sensitive.MessageText`.
- A parser change that alters `IdentityKey` changes transaction ids: bump the parser version and plan a migration.
- Transaction ids hash the *bank* account's identity, never the target account's: remapping or merging accounts must not break dedupe (ADR 203).
- Golden files: `go test ./internal/domain/importing/... -update`; fixtures are synthetic — real exports never enter the repo.
- `testdata/format-vectors.json` must stay byte-identical with `coin-book-web/packages/core/src/format-vectors.json`.
- Domain packages make no network calls (depguard); identifiers in English, UI copy lives in the web client.
