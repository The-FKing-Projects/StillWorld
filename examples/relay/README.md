# Relay example

Relay is a small, cooperative strategy game that demonstrates a downstream
game's use of StillWorld. Players salvage components and spend Turns to repair
a shared chain of signal stations. Completing a station unlocks the next one.
The game owns its actions, tables, migration, and executable; StillWorld owns
accounts, sessions, Turns, request IDs, and action transactions.

The server-rendered interface works without JavaScript, and a JSON API remains
available for clients. The game uses only public StillWorld packages; it does
not import any `internal` package.

## Run locally

From this directory, configure a disposable PostgreSQL database and a private
request-signing key:

```sh
export STILLWORLD_DATABASE_URL='postgres://...'
export RELAY_REQUEST_KEY="$(openssl rand -hex 32)"
export STILLWORLD_INSECURE_COOKIES=true
go run . migrate
go run . serve
```

The server binds to `127.0.0.1:8082` by default. Local development uses HTTP
only when `STILLWORLD_INSECURE_COOKIES=true`; production must use HTTPS and
secure cookies. Migrations are explicit, and serving refuses a schema that
does not match the game registered by this executable.
Open `http://127.0.0.1:8082/` to create an account and play.

New accounts start with the game's configured 20-Turn cap. Turns regenerate
one every 30 minutes. Salvaging costs one Turn and adds one component to the
player's pack, up to five. Repairing costs two Turns and two components, and
advances the shared active station by one step.

The HTML forms use the same authenticated action pipeline as the API. A
completed submission redirects to a player-scoped saved result. If the
outcome is uncertain, use the retry button to resend the original request;
do not start a new action until its outcome is understood.

## API loop

`GET /api/world` returns public station progress. Sign up and log in with the
existing `/signup` and `/login` endpoints, then issue and submit each action
with a fresh request ID:

```sh
curl -b cookies.txt -c cookies.txt \
  -H 'X-Stillworld-Request: 1' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'username=player_one&password=relay-test-password' \
  http://127.0.0.1:8082/signup

curl -b cookies.txt -c cookies.txt \
  -H 'X-Stillworld-Request: 1' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'username=player_one&password=relay-test-password' \
  http://127.0.0.1:8082/login

request_id="$(curl -sS -b cookies.txt -c cookies.txt \
  -H 'X-Stillworld-Request: 1' -X POST \
  http://127.0.0.1:8082/api/action-requests/relay.salvage | jq -r .request_id)"

curl -b cookies.txt -c cookies.txt \
  -H 'X-Stillworld-Request: 1' \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $request_id" \
  --data '{}' \
  http://127.0.0.1:8082/api/actions/relay.salvage
```

Use `relay.repair` in the same request-ID flow to contribute two components to
the current station. Share `/api/world` with other players to track collective
progress.

`presentation.go` implements the optional public `presentation.Game`
interface. The framework authenticates the player and issues form request IDs;
the game supplies page content, decodes action fields, and formats result text.
It never uses a client-supplied player ID as authentication.

## Checks

Run the module's tests and vet from this directory. The PostgreSQL-backed game
test runs when `STILLWORLD_DATABASE_URL` is set and creates and drops its own
temporary schema:

```sh
go test -count=1 ./...
go vet ./...
```
