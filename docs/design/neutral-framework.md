# Neutral framework boundary

Status: initial implementation contract. These decisions describe the intended
boundary, not an implemented or stable API. Turns arithmetic and the text
interface have separate design work remaining.

## Shared foundation

One StillWorld deployment hosts one persistent world. StillWorld provides
accounts and sessions, and is intended to provide a common action pipeline and
per-player Turns. Actions resolve immediately: validation, spending Turns,
and applying an action's effects must succeed or fail together in one database
transaction.

All games use that action model. A game defines its own actions, their costs,
entities, and effects; it does not replace or bypass the shared Turns and
transaction model. Each game configures its own Turns regeneration rate and
maximum balance. The specific values and configuration format are not yet
decided.

## Game-owned rules and data

Initially, game rules and entities are written in Go and compiled into the
game. This does not preclude data-driven content later. Games own their
database tables and migrations alongside StillWorld's tables in the same
database.

StillWorld will invoke game-defined actions within the shared transaction
through the public integration boundary described below. The current code
uses Go `internal` packages; those packages cannot already be imported by a
separate downstream repository.

StillWorld does not prescribe a setting, theme, or specific game entities.
Era of Darkness is planned as a separate private downstream game with its
own rules, content, and presentation.

## Public Go integration boundary

A downstream game owns its executable and registers its compiled Go actions
with StillWorld. It imports a small public action package rather than copying
upstream files or importing StillWorld's internal packages.

The action package exposes the types needed for authenticated player identity,
transaction-scoped database access, prepared actions, and results.
Authentication internals and transaction orchestration remain private;
public startup and registration entry points must allow a downstream
executable to use them without accessing internal packages.

Database access follows the existing standard-library `database/sql`
approach. Game code receives query and update methods backed by the current
transaction, using standard SQL parameter and result types, but no exposed
commit or rollback methods. Games use SQL for their own tables without a
new framework-specific database abstraction.

Use `github.com/The-FKing-Projects/StillWorld/action` for the action contract
and the module-root `stillworld` package for application composition. These
packages do not exist yet. Authentication implementations stay under
`internal`; a downstream game does not need a public password or session
service merely to run the application.

The composition API has this intended shape, with imports omitted:

```go
type Registration struct {
    Name    string
    Handler action.Action
}

type Game struct {
    ID         string
    Migrations fs.FS
    Actions    []Registration
}

func New(db *sql.DB, game Game, options Options) (*App, error)
func (app *App) Migrate(ctx context.Context) error
func (app *App) Handler(ctx context.Context) (http.Handler, error)
```

`Game.Migrations` is rooted directly at the embedded SQL files, for example
by using `fs.Sub`. One game is registered per deployment, not a collection of
runtime plugins. Game entities are ordinary game-owned Go types and SQL
tables; there is no generic entity registry or upstream ORM.

`Options` carries deployment settings, request-token verification keys, and
the game's Turns policy. Its fields will be finalized with the Turns and
deployment implementations, rather than introducing guessed regeneration
semantics here. The caller owns the database connection pool and HTTP server
and closes them on shutdown. `New` validates registration and configuration;
it does not apply migrations or start a listener.

The executable selects `Migrate` or `Handler` explicitly. `Handler` checks
schema readiness before returning the framework's authentication and action
HTTP handler; serving never upgrades the database automatically. Downstream
code may mount that handler alongside its own presentation routes.

The framework's HTTP entry point authenticates every action and supplies the
trusted identity internally. Do not expose a dispatch method that accepts an
arbitrary public `Player` as proof of authentication. A `Player` passed to
game code is a description, not a credential. Browser submissions retain the
framework's CSRF protections; request IDs do not replace them.

Registration rejects missing handlers, duplicate names, and invalid names at
startup. Game IDs use lowercase letters, digits, and underscores, beginning
with a letter, up to 32 ASCII characters. Action names use a game-specific
namespace such as `counter.increment`, up to 128 ASCII characters from
lowercase letters, digits, dots, and underscores, with no empty components.
Names and input/result meanings must remain compatible for outstanding
request IDs; an incompatible change uses a new action name. Check recorded
outcomes before resolving the current handler so a retained outcome remains
replayable even after its handler is removed.

These are API design signatures, not compilable declarations of an existing
library. Implementation must exercise them from a separate-module example
to prove that no `internal` import or upstream edit is needed.

## Migration composition

StillWorld and the game keep independent migration histories in the same
PostgreSQL database, using distinct version-tracking tables. Their migration
numbers do not share a namespace; each history may begin at `00001`.

The downstream executable embeds the game's migrations and supplies them
through a public integration API. One migration command applies pending
StillWorld migrations first, then game migrations. Game migrations may
reference framework tables, but must not alter framework-owned tables.

The combined upgrade is not guaranteed to be atomic. If a game migration
fails after framework upgrades succeed, those framework upgrades remain
applied. The command must report failure and deployment must wait until both
histories have been successfully applied. Recovery must respect the migration
runner's transaction guarantees rather than assume every failed migration
left no changes.

Keep existing framework tables (`accounts`, `sessions`) and the existing
`goose_db_version` history; do not rename applied migrations. Reserve
`stillworld_` for new framework tables. Game tables and explicitly named
indexes, sequences, and constraints use `game_` prefixes, with a separate
`game_goose_db_version` history. Both histories live in the deployment's
configured PostgreSQL schema. Goose providers must use separate stores, not
process-global settings or a merged migration filesystem.

Persist the registered game ID as framework deployment metadata and reject a
different ID against an initialized database. Migration numbers are positive,
unique within each history, and immutable after application. Migrations use
transactional SQL initially; reject Goose's nontransactional directive in the
composed runner until recovery for those migrations is explicitly supported.

Use Goose `WithTableName` for the respective migration histories. A
migration coordinator owns a dedicated PostgreSQL connection and holds one
session advisory lock across the entire combined upgrade, including all
preflight and final readiness checks. Goose's per-provider locking is
disabled; locking each history separately would leave a race between
framework and game migrations. All migration entry points must use this
coordinator. Acquisition is context-bounded; release on every exit and
discard the connection if unlocking cannot be confirmed. Serializing
unrelated schemas in one database is acceptable initially.

Goose providers execute through the pool while the coordinator connection
holds the advisory lock. Therefore reject a pool configured with only one
open connection; otherwise migration execution would wait for the
lock-holding connection. Account for the held connection in the pool
capacity.

Under the lock, check any recorded game identity before applying framework
migrations. After framework migrations ensure the metadata row exists and
bind an uninitialized database to this game ID, then verify that framework
versions exactly match this executable before applying game migrations. A
competing coordinator cannot intervene between stages. Final readiness
checks cover both histories. No game migration may run before identity and
framework compatibility checks succeed.

Run upgrades during a maintenance stop, not concurrently with game traffic.
An advisory lock coordinates migrators, not running servers. Serving verifies
both histories against the versions embedded in that executable, including
missing required versions and unexpected newer versions, and refuses to start
on a mismatch. Do not assume framework-first ordering makes an old game
compatible with every new framework release.

On failure, leave service stopped, inspect the reported history and version,
and resume pending migrations after correcting the cause. Never automatically
downgrade the framework to compensate for a game migration failure. Back up
before upgrading; backups and any manual recovery are operator actions, not
implicit behavior of the migration command. The existing command still
handles StillWorld migrations only.

## Action transaction ownership

StillWorld owns each action's database transaction lifecycle: it opens the
transaction and commits the Turns spend and game effects only when validation,
spending Turns, and applying effects all succeed. A failed action leaves no
Turns spend or game effects. A game-rule rejection may commit only its request
outcome, as described below; a technical failure rolls back the transaction.

Game actions receive a framework-supplied authenticated player identity and
must use the supplied transaction for all action-related database work. They
must not open an independent transaction or commit the supplied transaction.
Game code is trusted server code, not a sandboxed plugin; this is an
integration contract, not isolation from malicious game code.

The signatures and execution rules below specify the intended contract;
they do not imply that the orchestration exists yet.

## Action interface shape

The public action interface has Prepare and Apply stages within the same
framework-owned transaction:

1. Prepare validates submitted input, checks game permissions, protects
   relevant game state, and calculates the Turns cost. It must not persist
   gameplay changes.
2. StillWorld validates the calculated cost, checks the player's available
   Turns, and spends the required Turns within the transaction.
3. Apply performs the prepared game effects and returns a result. StillWorld
   records that result and commits only if the entire action succeeds.

The prepared action is transaction-local, not a client-held quote or a plan
that can be executed in a later transaction. State used during preparation
must remain protected through application; Apply must reject conflicting
state if preparation used optimistic checks rather than locks. A rejection
or technical failure in either stage leaves no Turns spend or game effects,
following the outcome rules below.

Game code receives transaction-scoped query and update methods without
exposed commit or rollback methods. This reduces accidental misuse, but does
not sandbox trusted game code. Neither stage may perform external effects,
such as sending email, that cannot be undone by a database rollback.

For the counter example, Prepare validates the requested increment and
calculates its cost; Apply updates the authenticated player's counter.
The following signatures define the initial implementation target.

### Go signature design

The following is the design for the public `action` package, not an implemented
or stable API. It uses `context`, `database/sql`, and `encoding/json` from the
standard library:

```go
type Player struct {
    ID int64
}

type DB interface {
    ExecContext(context.Context, string, ...any) (sql.Result, error)
    QueryContext(context.Context, string, ...any) (*sql.Rows, error)
    QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Action interface {
    Prepare(context.Context, Player, DB, json.RawMessage) (Prepared, error)
}

type Prepared struct {
    Cost  int64
    Apply func(context.Context, DB) (json.RawMessage, error)
}

type Rejection struct {
    Code    string
    Message string
}
```

`*Rejection` implements `error`; its message must be safe to show to the
player. StillWorld recognizes it using `errors.As`. Other errors are
technical failures, logged with context but not exposed as raw messages.
Errors from SQL must not be disguised as game-rule rejections.

The framework supplies a private transaction wrapper implementing `DB`, not
a raw `*sql.Tx` whose commit methods could be recovered by a type assertion.
The wrapper does not expose the underlying transaction. Game code must not
issue transaction-control SQL, retain the wrapper after the callback, or use
it in background goroutines. It must consume and close every `*sql.Rows`
before the callback returns. As elsewhere, this remains a trusted-code
contract rather than a security sandbox.

Each registered action implementation is shared and must be safe for
concurrent calls by different players. Per-request data belongs in the
returned prepared action, not mutable fields on the registered handler.

The agreed boundary uses JSON input and JSON result data without depending
on HTTP types. Each action decodes input into its own typed Go struct and
validates it during Prepare. HTML form adapters would build that input on
the server; this does not require JavaScript in the browser. Results are
data, not pre-rendered HTML, and must be encoded and stored successfully
before commit. Payload limits and input comparison are specified below.
Game-owned presentation renders results with normal template escaping,
never as trusted HTML supplied by the action.

StillWorld snapshots the returned cost, rejects negative costs and a nil
Apply callback as implementation errors, and does not let Apply revise the
cost. Zero permits a free action; positive costs must fit within the available
Turns. Use nonnegative `int64` whole-Turn costs, with checked arithmetic.
The existing database balance is `NUMERIC`; this interface does not silently
change its storage or specify how fractional regeneration becomes spendable.
That calculation is a prerequisite owned by the Turns workstream.

### Counter walkthrough

The game migration provisions counters for existing players; new players'
counters are initialized lazily by the action. For an action registered as
`counter.increment`, input might be `{"amount":3}`:

1. Prepare decodes and validates a positive integer amount, reads and locks
   the counter row if present using the supplied player's ID, treating absence
   as zero under the actor lock. It checks that adding the amount cannot
   overflow and returns a prepared action costing three Turns, with a callback
   capturing this request's validated data. All writers of this counter
   follow the actor-lock convention.
2. StillWorld checks and spends three Turns. If the balance is insufficient,
   it records a rejection without calling Apply or retaining gameplay changes.
3. Apply updates only the authenticated player's counter through the supplied
   DB, inserting the initial row if needed, and returns JSON containing the
   new counter value. Unexpected affected-row counts or SQL failures return
   errors, not success-shaped defaults.
4. StillWorld stores the result alongside the action's request ID and commits.
   A matching duplicate returns that stored value without calling Prepare
   or Apply again.

If Apply fails after spending Turns, that spend and any partial game changes
are rolled back. If it returns a game-rule rejection, only the rejection
outcome may survive. The persistence mechanism must preserve duplicate
exclusion while undoing gameplay changes; the signatures alone do not provide
that guarantee.

Registration associates the stable action name with an `Action` through the
composition API above. HTTP route names and result-page navigation belong to
the text-interface workstream, not this SQL/action contract.

## Action authorization

StillWorld establishes the acting player's identity from authentication, not
from a player ID supplied in the action input. Signing in does not grant
permission to act on arbitrary game entities.

Each game enforces ownership and game-specific permissions inside the action
transaction, using current state protected against conflicting changes.
Client-supplied target IDs identify requested targets, not permission to use
them. Games may define shared control or delegated permissions without
StillWorld prescribing those rules.

The neutral counter example permits an action to update only the
authenticated player's own counter.

## Action Turns costs

Game actions may have fixed Turns costs or calculate costs from submitted
input and current game state. The authoritative calculation happens in game
code within StillWorld's action transaction, not in the client. A cost shown
before submission is only an estimate.

StillWorld enforces the calculated cost against the player's available Turns.
The Turns spend and game effects must commit together or both roll back.

Games may define zero-Turn actions. These still use authentication,
authorization, the shared transaction, per-player serialization, and
duplicate-request protection. A zero cost does not exempt requests from
rate limiting; the action-specific rate-limit policy remains to be designed.
Negative costs are rejected as implementation errors, not used to award Turns.

## Action concurrency

StillWorld serializes actions for the same player by locking that player's
Turns row in PostgreSQL before calculating regeneration or invoking game
action code. It holds the lock until the transaction commits or rolls back.
A competing action waits for the lock and then uses the updated state.
Different players' actions may run concurrently.

The player lock protects the acting player's Turns, not every game-owned
resource. Game actions must also protect state used for authorization,
validation, cost calculation, and effects against conflicting changes.
They may use database row locks or conditional updates that reject stale
state; a failed condition must fail the action and roll back its Turns spend
and game effects. These protections must work across server processes, not
rely on in-memory locks.

Use PostgreSQL READ COMMITTED with explicit locks and conditional writes,
not an assumption that merely beginning a transaction freezes all reads.
Game code must protect predicates and missing rows too, using constraints,
conditional statements, or a stable parent row lock; locking only existing
rows does not prevent concurrent inserts.

After the acting player's row, acquire game-owned resource locks in a stable
documented order, such as table then primary key. Actions touching another
player's framework row can still form a deadlock because actor locks were
taken first. PostgreSQL deadlock detection and bounded waits are mandatory,
not a claim that resource ordering eliminates all deadlocks.

Initially set a five-second lock timeout and a ten-second total action
deadline, including waiting for the actor lock. Apply a database statement
timeout as well as a Go context deadline. Do not retry callbacks automatically
in the initial implementation: a confirmed deadlock or timeout rollback
returns a safe retryable error with instructions to reuse the same request ID.
Commit uncertainty is a separate outcome, not a confirmed rollback. Operators
may tune these limits through validated deployment options later.

## Duplicate action requests

Every game action submission requires a request ID (an idempotency key).
StillWorld issues an ID with a server-verified expiry when preparing a form
or action request. The form or client reuses that ID for double-clicks and
retries, including when a response is lost. A deliberately new action uses
a newly issued ID, even if its inputs are identical.

StillWorld scopes request IDs to the authenticated player and binds each
recorded ID to the action and its inputs. It records the successful result
in PostgreSQL in the same transaction as the Turns spend and game effects.
Game-rule rejections are also recorded, without a Turns spend or game effects.
Concurrent submissions with the same player and ID must not both execute
successfully.

A matching duplicate returns the recorded result without repeating game
effects or spending Turns again. Reusing a recorded ID with a different
action or different inputs is rejected. Retrieving a recorded result still
requires authentication as the owning player; the request ID is not a
credential.

Request IDs expire 24 hours after issuance; retries do not extend that
deadline. Within that window, matching retries return the saved result.
Recorded outcomes must remain available until the request ID expires
and may be cleaned up afterward.

Expired submissions are rejected, even if their result records have already
been deleted. Expiry must remain verifiable independently of those records;
a client-supplied timestamp alone is not sufficient. A form left open past
expiry must be refreshed before a new action can be submitted.

An expiry response does not imply that the original action failed. Clients
must not automatically replace an expired ID and resubmit the action.
Players should check current state before deliberately submitting a new
action.

### Request identity, input, and stored outcomes

Use an opaque, versioned request token containing a cryptographically random
nonce of at least 128 bits, authenticated player ID, registered game ID,
action name, issuance time, expiry, and verification-key ID. Authenticate
the entire unambiguous encoding with HMAC-SHA-256 and compare tags in constant
time. Tokens are issued only through authenticated, rate-limited framework
entry points, using database time. Verify scope, signature, supported version,
and the exact 24-hour lifetime; do not trust a timestamp simply because it
was decoded successfully.

Deployment supplies a shared verification-key ring and active issuance key.
Require at least 32 cryptographically random bytes per key, reject missing
or invalid configuration, and never generate a replacement on every restart.
Retain old verification keys until every token they signed has expired.
Removing a key early must reject its tokens, never reinterpret them as new.
All processes share keys and use the database clock for expiry decisions.
Never log tokens, signing secrets, or raw action input.

Bound decoded action input to 16 KiB and successful result data to 64 KiB.
Require one valid JSON object, reject duplicate object keys (including nested
objects) and trailing data, and let each action reject unknown fields and
validate its typed input. Integer fields decode directly into integer types,
not through floating-point intermediates.

Bind a request to its action name and exact submitted UTF-8 input bytes using
a SHA-256 fingerprint with unambiguous length-delimited fields. No JSON
canonicalization is promised: different whitespace or property order with
the same token is a conflict. Clients must preserve the original payload for
a retry. HTML adapters must serialize the same submitted field values
deterministically; a changed form requires a new token.

Store outcomes in a framework-owned table keyed uniquely by player ID and
request nonce, with action name, input fingerprint, expiry, status, and
either the bounded JSON result or safe rejection code/message. The player
ID references `accounts`; account deletion removes its outcomes. A success
contains no rejection and a rejection contains no success result. Responses
carry an outcome format version, and retries return the stored outcome rather
than rendering current game state as if it were the original result.

Require the handler result to be exactly one valid JSON value, with no
trailing data, and no larger than 64 KiB. Rejection codes use stable
namespaced ASCII identifiers up to 128 bytes and
messages are bounded to 1 KiB of valid UTF-8. Invalid or oversized handler
outputs are implementation errors and roll back, not silently truncated
successes. These initial limits bound storage per request; issuance and
submission also need bounded per-player and global admission limits. They
are operational controls, not additional Turns prices.

### Execution and cleanup ordering

1. Authenticate and enforce CSRF and admission limits. Validate the token,
   game identity, token-bound action name, and bounded JSON input before game
   code runs. This does not require the handler to remain registered.
   Authentication, malformed envelopes, expired tokens, and mismatched input
   are protocol errors, not stored game-rule rejections.
2. Begin the transaction, lock the actor's `accounts` row, then check expiry
   using current database time after the wait. Reject expired submissions
   before looking up or creating an outcome. Use wall-clock database time,
   not PostgreSQL's transaction-start `now()`, for this check.
3. Check for an existing outcome. Return it on a fingerprint match, or reject
   a conflict. Otherwise resolve the registered handler. Removed handlers
   do not stop replay of recorded outcomes for their bound action names.
4. Create a savepoint after the actor lock and before regeneration or any
   game work. Prepare, enforce the cost, Apply, and validate the result.
   On success, insert the outcome and commit all changes together.
5. On a game-rule rejection, roll back to the savepoint, retaining the actor
   lock acquired before it. Insert only the rejection outcome and commit.
   On technical error, roll back the entire transaction. If savepoint
   recovery or outcome persistence fails, treat it as a technical failure.

The unique outcome constraint is defense in depth; the actor lock serializes
the absent-record case. Rejection rollback must not release that outer lock.
An error from SQL that aborts the transaction cannot be turned into a
persisted rule rejection by catching and ignoring it.

An action admitted before expiry may finish after expiry within its bounded
deadline. Any later request that obtains the player lock after expiry is
rejected even if an earlier action succeeded. Cleanup deletes only committed
outcomes whose expiry is in the past, in bounded batches. It must acquire the
same account-row lock used by action execution before deleting that player's
outcomes. Select account IDs in stable order with `SKIP LOCKED`, then recheck
expiry using database wall-clock time and delete only expired outcomes for
those locked accounts within the same transaction.
This prevents cleanup from removing a committed result between an action's
expiry check and outcome lookup. It also prevents cleanup from racing an
in-flight action that was admitted before expiry.

Cleanup cannot see an uncommitted insert; one committed after a cleanup pass
is removed by a later pass. This is safe because no expired request is allowed
to execute anew. If cleanup is delayed, storage grows but replay protection
does not weaken.

## Rejected and failed attempts

Game-rule rejections, such as insufficient Turns or an unmet requirement,
are final outcomes for that request ID until expiry. A matching retry returns
the recorded rejection without reevaluating the action, even if game state
has changed. Trying again under changed conditions requires a deliberate new
submission with a newly issued ID.

Persisting a rejection must leave no Turns spend or game effects, including
any partial changes made before the rejection. Recording the rejection and
excluding competing executions must be atomic, so a delayed duplicate cannot
succeed after the rejection has been committed.

A confirmed technical rollback, such as a database deadlock, does not record
a final action outcome. The same ID and inputs may be retried within the
original expiry window. An uncertain commit outcome must not be reported as a
confirmed rollback: a retry uses the same ID to retrieve a committed outcome
or attempt execution if no outcome was committed, subject to the same
concurrency protections.

Technical error details belong in server logs, not player-facing responses.
Only a typed `*action.Rejection` from the handler or a framework rule check
is a final game-rule outcome. Technical failures have a safe framework code
and correlation ID, with details in logs; they never become stored business
rejections. Distinguish confirmed retryable rollback from unknown commit
outcome in responses. Neither permits automatically switching to a new token.

## Minimal example

Include a small neutral example game upstream: one action spends Turns to
change a persistent counter stored in a game-owned table. It should exercise
game registration, game migrations, and the shared action transaction once
those integration mechanisms exist. This is a minimal proof of the boundary,
not a full game or a demonstration of player-to-player interaction.

## Implementation acceptance criteria

The neutral example must run from a separate Go module using only public
imports. It provides its own executable, embedded transactional migrations,
and one counter action; no framework file is edited. Its table has a player
foreign key and a nonnegative `BIGINT` counter. Existing accounts are
provisioned by migration; Prepare may observe a missing row for a newly
registered player as zero, and Apply uses a checked insert/update under the
actor lock. A game permission rejection is not a substitute for provisioning
or schema failures.

The design is ready for implementation only with these checks as release
criteria, not claims about existing coverage:

| Scenario | Required outcome |
| --- | --- |
| Two different IDs spend the same player's last Turns | Only an affordable action succeeds; no negative balance |
| Concurrent matching submissions of one ID | One execution, identical stored outcome for retries |
| Same ID with changed bytes or action | Conflict, no game callback or second spend |
| Insufficient Turns, followed by regeneration and a retry | Same stored rejection; a new action requires a new ID |
| Apply changes a row then rejects | No counter or Turns change, rejection remains replayable |
| Apply or result serialization fails technically | Full rollback, no final outcome, same-ID retry permitted |
| Response is lost after commit | Same-ID retry returns the committed result |
| Request waits for a lock until after expiry | Rejected before game work, regardless of record cleanup |
| Cleanup races a duplicate lookup or pre-expiry action | Shared account lock prevents deleting an outcome used by that transaction |
| Deadline/deadlock or unconfirmed commit | Explicit technical/unknown outcome, never false success |
| Different players contend on shared game state | Locks/conditions preserve the game's invariant |
| Framework migrations succeed and game migration fails | Failed command, framework retained, serving refused |
| Concurrent migrators or a newer executable intervenes | Shared lock and version checks prevent conflicting DDL |
| Zero cost, negative cost, counter/cost overflow | Free action protected normally; invalid values never commit |
| Direct request tries to supply another player's identity | Framework identity wins; unauthorized game access denied |

Use actual PostgreSQL for transaction, savepoint, deadlock, cleanup, and
migration tests, including the repository's isolated per-test schemas. Add
fake-clock unit tests for token expiry and key rotation. Build the
separate-module example to verify the integration boundary.

## Remaining work and scope

The initial framework boundary design is covered here. Implementation is a
separate milestone and must not be inferred from design approval.

- Turns work (#20) specifies regeneration arithmetic, fractional balances,
  rate/cap configuration fields, and behavior when settings change. It is a
  prerequisite for implementing authoritative action spending.
- Text-interface work (#21) specifies action routes, ordinary HTML forms,
  result presentation, and safe retry UX, using these action contracts.
- Deployment and operational work (#22 and #23) specifies key provisioning,
  backups, maintenance upgrades, proxy configuration, and concrete admission
  limits before public exposure.
- Implement public composition, migration stores/locking, action execution,
  request protection, and the neutral example with the acceptance cases above.

The current code does not yet implement the action pipeline, Turns
regeneration, game integration API, or example game.
