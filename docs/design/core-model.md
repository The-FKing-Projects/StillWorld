# Core model: decisions so far

Status: draft. These are design decisions, not a finished specification.

## Decided

- **One world per deployment.** A deployment hosts a single persistent world
  in a single database. Hosting multiple worlds is out of scope for now.
- **Turns regenerate over time.** Each player's Turns accrue at a fixed rate up
  to a cap. Each game configures its own rate and cap. Turns are action costs,
  not rounds of turn-based combat. New accounts start at the configured cap.
- **Regeneration is computed on demand.** The database stores a player's
  balance and the time it was last updated. The current value is derived when
  read, so no background job ticks every player.
- **Actions resolve instantly.** An action is validated and applied in a single
  database transaction that also spends its Turns. Delayed or queued actions
  are out of scope for now.
- **Same-player actions are serialized.** StillWorld locks the acting player's
  Turns row for the action transaction. Games must also protect shared game
  state; see [Neutral framework boundary](neutral-framework.md).
- **Players use server-rendered HTML forms, with a JSON API for clients.**
  Both interfaces share the same authenticated action pipeline and
  request-ID retry rules; see
  [Neutral framework boundary](neutral-framework.md).

- **Accounts live in StillWorld's own database.** Players sign in with a
  username and password; external login providers are out of scope for now.
- **Email is optional** and used only for password reset. Accounts without an
  email cannot reset their own password and need the host's help.

- **Passwords are hashed with Argon2id.**
- **Sessions are server-side.** A random token in a cookie references a session
  stored in PostgreSQL, so sessions can be revoked immediately. Sessions use
  sliding expiry: 30 days since last activity, with a hard cap of 90 days.

## Still open

- Password reset flow and target-host Argon2id parameters.
- Main-executable configuration for choosing a downstream game and its Turns
  policy; the public composition API already accepts these settings.
- Maintain PostgreSQL-backed coverage of migration coordination, cleanup
  locking, and the action locking, retry, and duplicate-request implementation.
- Implementation of the documented HTML forms, player-facing routes, and
  result pages; the authenticated JSON action API exists as an initial
  implementation.
