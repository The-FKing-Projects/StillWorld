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

- **Passwords are hashed with Argon2id.** Staging defaults (issue #18 budget B)
  are 32 MiB memory, 3 iterations, and 2 lanes, with up to four concurrent
  hash slots in the HTTP layer (login, signup, and deferred signup hashing).
  Successful login rehashes when stored parameters are weaker on any axis,
  using the per-axis maximum of stored and current values so stronger axes
  are not downgraded. These are private/staging values, not a
  public-deployment clearance.
- **Sessions are server-side.** A random token in a cookie references a session
  stored in PostgreSQL, so sessions can be revoked immediately. Sessions use
  sliding expiry: 30 days since last activity, with a hard cap of 90 days.
- **Password recovery is implemented (issue #17).** Opt-in SMTP delivery is
  documented in [deployment](../deployment.md#optional-password-recovery).
  Recovery codes are single-use and short-lived: the service stores only token
  hashes, keeps at most one 30-minute token per account, and atomically
  replaces the password and revokes sessions. An administrator's password
  reset or account disable invalidates outstanding tokens; session-only
  revocation does not. Recovery requires a verified address.
- **Optional TOTP is implemented (issue #15, decision A).** Per-account
  authenticator enrolment stores the TOTP seed encrypted at rest with a
  config-provided AES-256-GCM key and `key_id` (see
  [deployment](../deployment.md#optional-totp-encryption-key)). Enrolment
  confirms a valid code first. Login becomes two steps when enabled: password,
  then TOTP or a hashed one-time recovery code. Verification uses a ±1 step
  window with replay protection. Code attempts share the login IP rate limit.
  Not production-ready.
- **Optional passkeys are implemented (issue #16, decision A).** Per-account
  WebAuthn credentials are a second factor only (not passwordless), using
  `go-webauthn` with public credential rows and short-lived ceremony sessions.
  Login reuses the TOTP `login_challenges` bridge; when both TOTP and a passkey
  are enrolled, either factor may complete the challenge. Ceremony JavaScript
  is confined to enrol/assert pages; game actions stay text-first without JS.
  Relying Party ID and origins come from config (see
  [deployment](../deployment.md#optional-passkeys-webauthn)). Not
  production-ready.

## Still open

- Password recovery and email verification use bounded admission (in memory by
  default or shared through PostgreSQL). Recovery request responses are generic
  for eligible and ineligible addresses; verification is signed-in and returns a
  distinct message when the account has no unverified address. These controls
  do not establish comprehensive enumeration or abuse resistance. Issue #23
  remediations landed (PR #45); concurrent password-hash, action, and delivery
  slots remain per-process by design for single-VPS staging—revisit only if
  multi-server production is planned. Not production-ready.
- Main-executable configuration for choosing a downstream game and its Turns
  policy; the public composition API already accepts these settings.
- Maintain PostgreSQL-backed coverage of migration coordination, cleanup
  locking, and the action locking, retry, and duplicate-request implementation.
- Extend the initial HTML forms and result presentation as games need richer
  inputs and views; the Relay example demonstrates the shared HTML and JSON
  action pipeline.
