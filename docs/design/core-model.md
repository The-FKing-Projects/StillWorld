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
- **Email is optional.** When present it supports signed-in address verification
  and, once verified, opt-in self-service password recovery. Accounts without a
  usable verified address cannot reset their own password and need the host's
  help (`stillworld reset-password`).

- **Passwords are hashed with Argon2id.**
- **Sessions are server-side.** A random token in a cookie references a session
  stored in PostgreSQL, so sessions can be revoked immediately. Sessions use
  sliding expiry: 30 days since last activity, with a hard cap of 90 days.
- **Password recovery is implemented (issue #17).** Opt-in SMTP delivery is
  documented in [deployment](../deployment.md#optional-password-recovery).
  Recovery codes are single-use and short-lived: the service stores only token
  hashes, keeps at most one 30-minute token per account, and atomically
  replaces the password and revokes sessions. An administrator's password
  reset, session revocation, or account disable invalidates outstanding
  tokens. Recovery requires a verified address.

## Still open

- Recovery and verification use bounded admission (in memory by default or
  shared through PostgreSQL) and generic request responses; these controls do
  not establish comprehensive enumeration or abuse resistance. Remaining
  authentication and rate-limit remediations are tracked in issue #23
  (documenting a limitation is not the same as remediating it).
- Target-host Argon2id latency, memory, and concurrency budgets remain
  undecided; see issue #18 before retuning parameters.
- Main-executable configuration for choosing a downstream game and its Turns
  policy; the public composition API already accepts these settings.
- Maintain PostgreSQL-backed coverage of migration coordination, cleanup
  locking, and the action locking, retry, and duplicate-request implementation.
- Extend the initial HTML forms and result presentation as games need richer
  inputs and views; the Relay example demonstrates the shared HTML and JSON
  action pipeline.
- Optional TOTP (#15) and passkeys (#16) are not implemented; passkeys are
  intended after TOTP.
