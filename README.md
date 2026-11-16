# Daemon

The daemon is the backend of the Cyber&nbsp;ICE&nbsp;Box platform. It is one Go service that keeps users, events,
tasks and the task catalog, and serves the HTTP API for every platform frontend. It drives the
laboratory through one gRPC API: the platform runs on top of the
laboratory, while the laboratory also works on its own.

## Contents

- [What it does](#what-it-does)
- [How it is built](#how-it-is-built)
- [Requirements](#requirements)
- [Run locally](#run-locally)
- [Configuration](#configuration)
- [Development](#development)
- [Releases](#releases)
- [License](#license)

## What it does

| Area | What you get |
|---|---|
| Accounts and sign-in | Registration, password and Google sign-in, passkeys, sessions, bot check (Turnstile or reCAPTCHA), roles and permissions |
| Task catalog | Tasks with files, hints and flags; import and export of task archives; test labs for authors |
| Events | Events with their own address, pages, forms, teams, participants, stages, task groups and managers |
| Labs and stands | Starts labs for events and test runs through the laboratory; VPN configs and access links; lab checks and monitoring |
| Scoring | Flag submissions, attempts, hints, scoreboards, results and live screens |
| Mail and notifications | SMTP providers, templates, broadcasts, site banners, the in-app inbox, signals and delivery journals |
| Analytics | Platform and per-event analytics with PDF and CSV reports |
| Resource planning | Calendar of lab capacity and reservations for events |
| Administration | Platform settings, image registry, laboratory agents, audit log, error journal, key rotation, data retention |
| Authoring assistant | Optional link to an external AI service that helps authors build tasks |

Background work (lab start and cleanup, mail delivery, retention, import and export, garbage collection) runs as
River jobs on the same PostgreSQL database (`internal/jobs`).

## How it is built

| Part | Path | Role |
|---|---|---|
| Model | `internal/model` | Entities, domain rules and domain errors. It imports nothing from the layers above. |
| Use cases | `internal/useCase` | Orchestration: load an entity, change it through its methods, save it. |
| Delivery | `internal/delivery` | HTTP controllers and handlers, PostgreSQL repositories, clients for the laboratory agents and the assistant. |
| Jobs | `internal/jobs` | River workers and their registry. |
| Config and start-up | `internal/config`, `internal/app` | Environment parsing and validation; wiring. |
| Shared packages | `pkg` | Errors, pagination, passwords, tokens, secret sealing, storage, mail, OAuth and other small libraries. |
| Commands | `cmd/daemon`, `cmd/seed`, `cmd/loadtest-users` | The service, the test-data seeder, a load-test helper. |
| Checks | `tools` | Lint tools for error codes, layer imports, route gates and secret contexts; the error-catalog generator. |

- **Storage.** PostgreSQL holds all state. Queries are SQL files turned into Go by sqlc. Migrations are numbered pairs
  in `internal/delivery/repository/postgres/migrations`, applied on start. Files and images go to an S3-compatible
  object store.
- **Frontends.** One REST API under `/api`, an OpenAPI spec generated from handler annotations, and server-sent
  events for live pages. Every route sits behind a permission check (`make vet` fails on a route without one). Errors
  carry numeric codes; the English and Ukrainian texts are in `error-catalog`.
- **Laboratory.** Laboratory agents are enrolled and then called over gRPC with mutual TLS. The agent contract comes
  from the laboratory Go module pinned in `go.mod`.
- **Replicas.** Any number of replicas can run; they share only the database. One of them, chosen with a PostgreSQL
  advisory lock (`internal/leader`), runs the polling loops.

## Requirements

- Go 1.27 (see `go.mod`).
- PostgreSQL.
- An S3-compatible object store and an SMTP server, if you need uploads and mail.
- Docker, for `make sqlcGenerate` and for the integration tests (they skip themselves when Docker is missing).

## Run locally

1. Start PostgreSQL, plus an object store and a mail catcher if you need them.
2. Create a git-ignored `.env` in the repository root with the settings below. Never commit it.
3. Start the daemon:

```bash
make run-local   # loads ./.env, then runs go run ./cmd/daemon
```

The daemon reads settings from the process environment only, which is why `make run-local` loads the file first.
Set `ENV=development` for a local run; the default is `production`. The service answers on `api.<DOMAIN>`, so route
that host to the process or send the `Host` header. `GET /api/health` is a public liveness probe. Outside
`production` the Swagger UI is served at `/docs`.

`make seed` fills a development database with a test event, teams and tasks (`make seed ARGS="--delete"` removes
them). It refuses to run with `ENV=production`.

## Configuration

All settings are environment variables; there is no example env file. The names, defaults and rules are in
`internal/config` (start with `internal/config/config.go`). The daemon refuses to start on a missing or invalid
required value.

| Variable | Meaning |
|---|---|
| `ENV` | `production` (default), `stage` or `development` |
| `DOMAIN` | The one base domain; every other host is derived from it |
| `SUPPORT_EMAIL` | Default Reply-To and contact in the mail footer |
| `POSTGRES_PASSWORD` | Database password (host, port, user and database have defaults) |
| `CAPTCHA_PROVIDER` | `turnstile`, `recaptcha` (default) or `none`; `none` is refused in production |

Everything else is optional with a default: HTTP and TLS listeners, SMTP, object storage, Google sign-in, laboratory
agent bootstrap, rate limits and data retention periods.

## Development

| Command | What it does |
|---|---|
| `make build` | Compiles all packages |
| `make test` | Runs all tests |
| `make vet` | `go vet` plus the project lints (error codes, layer imports, route gates, secret contexts) |
| `make sqlcGenerate` | Regenerates the Go code for SQL queries (sqlc, in Docker) |
| `go generate ./internal/delivery/repository/postgres/` | Regenerates the repository mocks |
| `make swagger` | Regenerates the OpenAPI spec |
| `make error-catalog` | Regenerates `error-catalog/errors.en.json` after an error was added or renamed |
| `make error-catalog-check` | Fails when the error catalog drifts from the code |
| `make tidy` | `go mod tidy` |

Rules to keep in mind:

- Edit the SQL in `internal/delivery/repository/postgres/queries`, then regenerate. Never edit generated files.
- Add a Ukrainian text to `error-catalog/errors.uk.json` for every new error.
- The laboratory module version is pinned in `go.mod`. To build against a local `../laboratory`, run `make work`.

The branch cycle (`make dev-start`, `make dev-push`, `make dev-done`) is described in [CONTRIBUTING.md](CONTRIBUTING.md).

## Releases

Branches, pull requests, release candidates, promotion and the retention of old releases are described in
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
