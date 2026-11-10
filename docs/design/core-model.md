# Core model: decisions so far

Status: draft. These are design decisions, not a finished specification.

## Decided

- **One world per deployment.** A deployment hosts a single persistent world
  in a single database. Hosting multiple worlds is out of scope for now.
- **Turns regenerate over time.** Each player's Turns accrue at a fixed rate up
  to a cap. Each game configures its own rate and cap. Turns are action costs,
  not rounds of turn-based combat.
- **Regeneration is computed on demand.** The database stores a player's
  balance and the time it was last updated. The current value is derived when
  read, so no background job ticks every player.
- **Actions resolve instantly.** An action is validated and applied in a single
  database transaction that also spends its Turns. Delayed or queued actions
  are out of scope for now.
- **Same-player actions are serialized.** StillWorld locks the acting player's
  Turns row for the action transaction. Games must also protect shared game
  state; see [Neutral framework boundary](neutral-framework.md).

- **Accounts live in StillWorld's own database.** Players sign in with a
  username and password; external login providers are out of scope for now.
- **Email is optional** and used only for password reset. Accounts without an
  email cannot reset their own password and need the host's help.

- **Passwords are hashed with Argon2id.**
- **Sessions are server-side.** A random token in a cookie references a session
  stored in PostgreSQL, so sessions can be revoked immediately. Sessions use
  sliding expiry: 30 days since last activity, with a hard cap of 90 days.

## Still open

- Password reset flow details and Argon2id parameters.
- Cookie and CSRF protections.
- Implementation of the game-owned entity and action boundary; see
  [Neutral framework boundary](neutral-framework.md).
- Game configuration integration (the internal Turns policy and policy-change
  semantics are implemented; see [Turns API](turns.md)).
- Implementation of the documented locking, retry, and duplicate-request rules.
- The text interface: HTTP shape, rendering, and input.
