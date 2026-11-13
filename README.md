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
[deployment guide](docs/deployment.md). It documents the current operational
limits and does not change the project's public-deployment readiness status.

## Clean-room development

StillWorld is developed independently. Other games may inform broad
genre-level ideas only; do not derive code, schemas, templates, prose, assets,
or implementation details from material you are not licensed to use.
