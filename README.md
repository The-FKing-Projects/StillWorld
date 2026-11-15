<img src="assets/stillworld-mark.svg" alt="StillWorld mark" width="96" height="96">

# StillWorld

An open-source foundation for persistent, text-first multiplayer strategy games.

## Purpose

StillWorld is an early-stage project exploring a foundation for persistent
multiplayer worlds. Its intended experience is primarily text-based, with
minimal visuals. Players are expected to take asynchronous actions that spend
from a shared action-point pool called **Turns**. Turns represent action costs,
not rounds of turn-based combat.

## Status

StillWorld is in its early stages. Accounts, server-side sessions, experimental
HTTP authentication, PostgreSQL migrations, internal Turns accounting, and an
initial action contract, execution engine, JSON API, public composition API,
and coordinated game migrations are implemented as early foundations. There is
not yet a configured production game. The Relay example has a minimal
server-rendered player interface and is not production-ready. The
PostgreSQL-backed action and composed-migration tests pass against a disposable
local database. This is not a feature-complete system.

Optional email-based password recovery is available for accounts with an email
address. It is disabled until configured; see the
[deployment guide](docs/deployment.md#optional-password-recovery) for setup and
limitations.

The HTTP endpoints are experimental and are not yet ready for public
deployment. State-changing requests must include a matching `Origin`,
same-origin Fetch Metadata, or `X-Stillworld-Request: 1` for non-browser clients.
Cross-site Fetch Metadata is rejected even when the custom header is present.
The expected public scheme is HTTPS by default, including behind a TLS
terminating proxy; `STILLWORLD_INSECURE_COOKIES=true` switches it to HTTP for
local development only. When using `STILLWORLD_BEHIND_PROXY=true`, restrict
direct access to the application and ensure the trusted proxy sets the
rightmost `X-Forwarded-For` value. The proxy must also preserve the public
`Host` header, including any port; the origin check does not use
`X-Forwarded-Host`.

For a private or staging single-VPS setup, see the
[deployment guide](docs/deployment.md). That guide deploys the **Relay**
example as the game-owning executable (not `cmd/stillworld`, which is auth and
operator tooling only). It documents the current operational limits and does
not change the project's public-deployment readiness status.

## Quick start

Requirements: Go version from [`go.mod`](go.mod), and PostgreSQL for integration
tests and local serve.

```sh
# Unit and integration tests (CI uses a disposable Postgres service)
export STILLWORLD_DATABASE_URL='postgres://stillworld:stillworld@localhost:5432/stillworld_test?sslmode=disable'
go test ./...

# Relay example (game-owned binary) for local smoke — insecure cookies for HTTP
cd examples/relay
# See examples/relay/README.md for migrate/serve and env vars
```

`cmd/stillworld` is auth and operator tooling only; it does not register a game.
Downstream games own the deployable binary (Relay for staging docs).

## Docs

| Doc | Role |
| --- | --- |
| [PRODUCT.md](PRODUCT.md) | Product purpose and constraints |
| [DESIGN.md](DESIGN.md) | Design entry points |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Clean-room rule and DCO |
| [docs/deployment.md](docs/deployment.md) | Private/staging single-VPS (Relay) |
| [docs/design/](docs/design/) | Core model, Turns, neutral framework |

## Roadmap

Living index: [issue #24](https://github.com/The-FKing-Projects/StillWorld/issues/24).  
Phase milestones: [milestones](https://github.com/The-FKing-Projects/StillWorld/milestones).

Open work is tracked as discrete issues under **Staging readiness**. StillWorld
is **not production-ready**.

## Clean-room development

StillWorld is developed independently. Other games may inform broad
genre-level ideas only; do not derive code, schemas, templates, prose, assets,
or implementation details from material you are not licensed to use.

## License

[MIT](LICENSE)
