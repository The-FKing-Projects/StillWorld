# Counter example

This is a separate Go module showing how a game can register an action and
compose StillWorld without importing any `internal` package. It uses its own
`game_`-prefixed migration and embeds that migration into the executable.

With PostgreSQL available, set `STILLWORLD_DATABASE_URL` and a private
32-byte-or-longer request-signing key encoded as hex:

```sh
export STILLWORLD_DATABASE_URL='postgres://...'
export COUNTER_REQUEST_KEY="$(openssl rand -hex 32)"
go run . migrate
go run . serve
```

The server binds to `127.0.0.1:8081` by default and uses insecure cookies for
local HTTP development. Do not expose it directly to the internet; production
deployments should terminate TLS and configure secure cookies at the app layer.
Migrations are explicit; serving checks schema readiness and will not migrate
automatically.

Run the module's compile-time and focused action tests with `go test ./...`
from this directory. The request signing key must remain stable until every
request ID it signed has expired.
