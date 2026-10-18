# coin-book-api

[![ci](https://github.com/coin-book/coin-book-api/actions/workflows/ci.yml/badge.svg)](https://github.com/coin-book/coin-book-api/actions/workflows/ci.yml)
[![release](https://github.com/coin-book/coin-book-api/actions/workflows/release.yml/badge.svg)](https://github.com/coin-book/coin-book-api/actions/workflows/release.yml)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/postgresql-18-4169E1?logo=postgresql&logoColor=white)
![OpenAPI](https://img.shields.io/badge/openapi-3.1-6BA539?logo=openapiinitiative&logoColor=white)
[![image](https://img.shields.io/badge/image-ghcr.io%2Fcoin--book%2Fcoin--book--api-2496ED?logo=docker&logoColor=white)](https://github.com/coin-book/coin-book-api/pkgs/container/coin-book-api)

REST API of coin-book, a self-hosted personal finance app. It imports bank statements (mBank, Revolut,
a generic CSV), categorises transactions, pairs transfers, keeps budgets and investments, and serves
it all to the web client and to AI agents. Every user sees only their own data (PostgreSQL row-level security).

## Run

The whole stack runs from [coin-book-local](https://github.com/coin-book/coin-book-local): `make api` there starts Postgres and
this API on http://127.0.0.1:8082, with interactive docs at `/api/docs`. The API migrates the database on start.

Configuration is `config/config.yaml`, overridden by `COINBOOK_*` environment variables
(`COINBOOK_JWT_SECRET` and `COINBOOK_POSTGRES_PASSWORD` are required).

## Develop

```sh
make test              # unit and golden tests
make test-integration  # acceptance tests against Postgres in Docker
make lint
make openapi           # regenerate openapi.yaml after changing a handler
```

Go 1.27, chi + Huma, pgx, golang-migrate. Decisions live in [coin-book-docs](https://github.com/coin-book/coin-book-docs).
