# Core model: decisions so far

Status: draft. These are design decisions, not a finished specification.

## Decided

- **One world per deployment.** A deployment hosts a single persistent world
  in a single database. Hosting multiple worlds is out of scope for now.
- **Turns regenerate over time.** Each player's Turns accrue at a fixed rate up
  to a cap. Turns are action costs, not rounds of turn-based combat.
- **Regeneration is computed on demand.** The database stores a player's
  balance and the time it was last updated. The current value is derived when
  read, so no background job ticks every player.
- **Actions resolve instantly.** An action is validated and applied in a single
  database transaction that also spends its Turns. Delayed or queued actions
  are out of scope for now.

- **Accounts live in StillWorld's own database.** Players sign in with a
  username and password; external login providers are out of scope for now.
- **Email is optional** and used only for password reset. Accounts without an
  email cannot reset their own password and need the host's help.

## Still open

- Password hashing, session handling, and password reset flow details.
- What a world contains (the entity model) beyond players and Turns.
- Regeneration rate and cap: game-level configuration or fixed by the engine.
- Concurrency rules when one player submits actions at the same time.
- The text interface: HTTP shape, rendering, and input.
