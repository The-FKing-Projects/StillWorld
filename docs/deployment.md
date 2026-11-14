# Single-VPS deployment

This guide describes a Debian or Ubuntu single-server setup using PostgreSQL,
systemd, and Nginx as the TLS-terminating reverse proxy. Adapt package and
certificate commands for other distributions.

> **Deployment status:** StillWorld's HTTP authentication endpoints are
> experimental and are not ready for public deployment. Use this procedure for
> private or staging environments only. Do not expose the service to the public
> internet until the project has explicitly cleared the remaining security and
> operational readiness work.

## Host and database

Use a supported Linux release, keep it patched, and allow inbound traffic only
to SSH and the reverse proxy's HTTP/HTTPS ports. PostgreSQL and StillWorld should
not be reachable from the public network. Keep PostgreSQL bound to loopback (or
a local Unix socket) and firewall the application port as well.

Install PostgreSQL and Nginx on the server, and install the Go version required
by `go.mod` on the build host. Create a dedicated PostgreSQL login and
database; enter a randomly generated password at the prompt rather than
putting it in a shell command:

```sh
sudo -u postgres createuser --pwprompt stillworld
sudo -u postgres createdb --owner=stillworld stillworld
```

For the example connection URL below, use a password made only of hexadecimal
characters (for example, generate one with `openssl rand -hex 32`). If a
different password contains URL-reserved characters, percent-encode it before
placing it in the URL. Restrict the database role to this database; do not use
the PostgreSQL superuser for the application.

StillWorld currently needs no application master, signing, or encryption key.
It generates random session tokens and stores only their hashes in PostgreSQL.
Provision and protect the database credential, and protect the database and
its backups; do not invent or reuse an application secret.

## Configure and install

Create a system account and a root-managed environment file readable only by
that account:

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin stillworld
sudo install -d -o root -g stillworld -m 0750 /etc/stillworld
sudo install -o root -g stillworld -m 0640 /dev/null /etc/stillworld/stillworld.env
sudoedit /etc/stillworld/stillworld.env
```

Put the following settings in `/etc/stillworld/stillworld.env`, replacing
`HEX_PASSWORD` with the generated database password:

```ini
STILLWORLD_DATABASE_URL=postgres://stillworld:HEX_PASSWORD@127.0.0.1:5432/stillworld?sslmode=disable
STILLWORLD_LISTEN_ADDR=127.0.0.1:8080
STILLWORLD_BEHIND_PROXY=true
STILLWORLD_INSECURE_COOKIES=false
```

`STILLWORLD_DATABASE_URL` is required. The listen address defaults to
`127.0.0.1:8080`; set it explicitly here so the service is not exposed beyond
the host. `STILLWORLD_BEHIND_PROXY=true` is appropriate only with the trusted
Nginx configuration below. The application trusts the rightmost
`X-Forwarded-For` value for rate limiting, so do not enable this setting if
untrusted clients can connect directly or supply that header. Never set
`STILLWORLD_INSECURE_COOKIES=true` on a remotely reachable deployment.

Build the binary from a reviewed source revision on a trusted build host, then
transfer it to the server through a trusted channel:

```sh
go build -trimpath -o stillworld ./cmd/stillworld
sudo install -o root -g root -m 0755 stillworld /usr/local/bin/stillworld
```

Create `/etc/systemd/system/stillworld-migrate.service`:

```ini
[Unit]
Description=Apply StillWorld database migrations
Wants=network-online.target
After=network-online.target postgresql.service
Requires=postgresql.service

[Service]
Type=oneshot
User=stillworld
Group=stillworld
EnvironmentFile=/etc/stillworld/stillworld.env
ExecStart=/usr/local/bin/stillworld migrate
```

Create `/etc/systemd/system/stillworld.service`:

```ini
[Unit]
Description=StillWorld HTTP service
Wants=network-online.target
After=network-online.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=stillworld
Group=stillworld
EnvironmentFile=/etc/stillworld/stillworld.env
ExecStart=/usr/local/bin/stillworld serve
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
UMask=0077

[Install]
WantedBy=multi-user.target
```

Apply migrations explicitly before starting the service; `serve` does not
automatically migrate the database:

```sh
sudo systemctl daemon-reload
sudo systemctl start stillworld-migrate.service
sudo systemctl enable --now stillworld.service
sudo systemctl --no-pager --full status stillworld.service
```

Check `journalctl -u stillworld.service` if startup fails. Keep the environment
file out of source control and backups that are not protected as secrets.

## TLS reverse proxy

Provision a certificate for the public hostname with a trusted certificate
manager. Configure Nginx to redirect HTTP to HTTPS and proxy HTTPS requests to
the loopback-only application. Replace the hostname and certificate paths:

```nginx
server {
    listen 80;
    server_name play.example.net;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name play.example.net;

    ssl_certificate     /etc/letsencrypt/live/play.example.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/play.example.net/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

The explicit `Host` setting preserves the public host (including a port, if
present) for the application's origin checks. Nginx replaces rather than
appends `X-Forwarded-For`, preventing a client-supplied value from becoming the
trusted rightmost address. This assumes Nginx is the public-facing proxy. If
another load balancer or CDN is in front, configure and validate its trusted
client-IP handling before enabling `STILLWORLD_BEHIND_PROXY`.

Validate and reload Nginx (`nginx -t`, then reload its service). Confirm the
app listens only on loopback, and that firewall rules do not allow direct
access to port 8080 or PostgreSQL. Do not rely on `X-Forwarded-Host`; the
application uses the preserved `Host` header.

## Admission limits

The current limits are fixed in the application and are not configurable by
environment variables:

| Operation | Limit |
|---|---|
| Concurrent login/signup password-hash work | 4 requests; excess requests receive `503` |
| Failed logins per client IP | 20 failures per 15-minute window |
| Failed logins per case-insensitive username | 10 failures per 15-minute window |
| Signup requests per client IP | 5 per hour |
| Password recovery requests per client IP | 10 per hour |
| Password recovery requests per email address | 1 per 10 minutes |
| Concurrent recovery and verification delivery attempts | 16; excess attempts are logged |
| Password reset submissions per client IP | 20 per 15 minutes |
| Email verification requests per client IP / account | 10 per hour / 1 per 10 minutes |
| Email verification submissions per client IP / account | 20 / 10 per 15 minutes |

By default, these limits are per-process and in memory: they are not shared
across servers and restart with the process. Successful login clears the
username failure counter. The password-hash work uses Argon2id with
64 MiB per operation by default, so four simultaneous operations can consume
about 256 MiB for hashing alone, in addition to PostgreSQL, the application,
and the operating system. Load-test the target host and monitor resource use;
do not treat these initial limits as a guarantee of public-service capacity or
abuse resistance.

For shared, restart-persistent login, signup, password recovery, and email verification limits,
apply the latest migrations and set `STILLWORLD_SHARED_RATE_LIMITS=true` on
every instance using the same database schema. The public composition API can
also enable `stillworld.Options.SharedRateLimits`. Instances using the default
in-memory mode do not participate in the shared limits. Action limits,
concurrent password-hash slots, and delivery slots always remain per-process.

The shared store uses database time and fails closed with `503` if it is
unavailable or its combined 100,000-key capacity is exhausted. It stores
SHA-256 hashes of identifiers, not raw usernames, addresses, or IPs; hashes
are not anonymization and database access still requires protection. Expired
entries are removed in bounded batches as requests arrive and may remain
stored during idle periods. A process that stops during an in-flight login may
leave its reservation counted until the window expires. Admissions serialize
on a database row, so monitor PostgreSQL and load-test before enabling this
experimental option. Keep the migration in place while any instance uses it.

On the target host, the optional `stillworld benchmark-password` command
measures password hashing without connecting to a database. It defaults to
three sequential hashes with the current default parameters. For example,
`stillworld benchmark-password -concurrency=4` measures batches of four
simultaneous hashes, matching the server's current limit. Run it in a
controlled environment: it consumes CPU and memory and can affect a running
service. Use `-memory-kib`, `-iterations`, `-parallelism`, and `-samples` to
compare settings; the command reports per-hash timings and estimated hashing
memory. Measurements vary with load, and a few hundred milliseconds per hash
is a starting target, not a security or capacity guarantee. The command does
not change the application configuration; Argon2id parameters are currently
compiled defaults. Do not lower them solely to hit a timing target.

The per-username login limit can be deliberately exhausted by someone who
knows a username, preventing that account from logging in until its 15-minute
window expires. Signup returns distinct errors for an already-used username
and email address, so it reveals whether an email is registered. Replacing the
email-conflict response with a generic success message alone would not remove
that signal while signup immediately creates usable accounts: attempting to
log in with the submitted credentials reveals whether creation succeeded, and
retrying a successful signup reveals the username conflict. Email verification
or another flow change would be needed to avoid those signals; signup does not
currently verify email addresses.

An administrator with database access can list account IDs, usernames, and
account status with `stillworld list-accounts`, using `STILLWORLD_DATABASE_URL`
from the protected service environment. It omits email addresses and prints
usernames as quoted, escaped strings. The command displays up to 100 accounts
per page; follow its `-after` hint to continue, or set `-limit` from 1 to 1000.
Treat the output as account data and protect it accordingly.

An administrator with access to the application database can reset an
account's password locally:

```sh
stillworld reset-password USERNAME
```

Set `STILLWORLD_DATABASE_URL` through the protected service environment first.
The command requires an interactive terminal and prompts twice without echoing
the password; do not put a password in shell arguments, logs, or scripts. A
successful reset revokes that account's existing sessions. Requests already
authenticated before the reset may still finish. If the command reports an
unknown outcome, retry with the same password; do not assume the reset failed.
This is an administrative recovery mechanism; independently verify the account
owner's identity before using it.

Use `stillworld disable-account USERNAME` to prevent login and revoke existing
sessions, and `stillworld enable-account USERNAME` to allow login again.
Enabling does not restore revoked sessions. Requests authenticated before a
disable may still finish. If a status change reports an unknown outcome,
check `list-accounts` before retrying. Apply the latest framework migrations
using the appropriate game migration command before deploying this version.
Stop older application instances before using these controls: older versions
do not enforce the disabled status.

To sign an account out without changing its password or status, run
`stillworld revoke-sessions USERNAME`. This revokes its current sessions, but
does not invalidate recovery codes or prevent new logins; a concurrent login
may also create a session after revocation. Already authenticated requests may
still finish. If the result is unknown, retry only if revoking any new sessions
is acceptable. If credentials or recovery codes may be compromised, reset the
password or disable the account instead.

## Optional password recovery

Self-service recovery is disabled by default. To enable it in the main server
or either example, add these settings to the protected service environment:

```ini
STILLWORLD_RECOVERY_ORIGIN=https://play.example.net
STILLWORLD_SMTP_ADDRESS=mail.example.net:587
STILLWORLD_SMTP_FROM=noreply@example.net
STILLWORLD_SMTP_MODE=starttls
STILLWORLD_SMTP_USERNAME=YOUR_SMTP_USERNAME
STILLWORLD_SMTP_PASSWORD=YOUR_SMTP_PASSWORD
```

Use the real public HTTPS origin without a path; it is used instead of the
request's `Host` to construct reset links. Plain HTTP origins are permitted
only for loopback development, and must match the server's cookie/CSRF scheme.
The counter example always uses development cookies, so its recovery origin
must be a loopback HTTP origin.
SMTP uses verified TLS 1.2 or newer. `starttls` is the default; set the mode to
`tls` for implicit TLS. Username and password must either both be set or both
be omitted. The origin, SMTP address, and sender are required when any
recovery setting is supplied. Protect the SMTP credentials like the database
credential. A custom game may instead provide a `recovery.Config` and its own
`recovery.Sender` to `stillworld.Options`; that sender must protect its own
transport and honor context cancellation.
Custom senders must implement both `SendReset` and `SendVerification`.

The HTML game interface links to the recovery forms; the main server also
serves these forms when recovery is enabled. API clients may submit
form-encoded `email` to `POST /forgot-password`, and `code`, `password`, and
`confirm_password` to `POST /reset-password`, subject to the same origin
protections as other state-changing requests. Codes are sent in the email body,
not the link; they expire after 30 minutes and are single-use. A successful
reset revokes existing sessions. Requests already authenticated may still
finish. If the result is unknown, try the new password or request another
code rather than assuming it failed.

Password recovery requires a verified address. With email delivery configured,
players sign in and use **Email verification** on the game page, or visit
`/verify-email` while signed in. API clients can POST to
`/verify-email/request`, then POST `code` to `/verify-email`. Codes are sent
in the email body, expire after 30 minutes, and work only for the signed-in
account and its unchanged address. Verification requests share the bounded
delivery slots with password recovery; delivery is best effort. A new code
replaces the previous one. There is currently no self-service email change;
an incorrect or inaccessible address requires administrator assistance.

**Upgrade:** stop all old server instances before applying migration 00008 and
start only updated instances afterward. All existing addresses start
unverified and all outstanding reset codes are invalidated. Existing users must
sign in and verify their address before using recovery; users who cannot sign
in need an administrator. Do not mix old and updated server versions: older
servers can issue resets without checking verification. Downgrade is blocked
while any account has an email address, and reverting to older code would
restore its weaker recovery policy.

Request responses are generic for eligible and ineligible addresses. Delivery
is asynchronous and best effort: limits, capacity, process shutdown, mail
failures, or a full mailbox can prevent delivery. A newer code invalidates an
older one. Monitor logs for recovery issuance, delivery, and capacity failures;
logs intentionally omit addresses and codes. Limits may prevent repeated
requests for the same address for 10 minutes, including when delivery fails.
They are per-process unless shared limits are enabled as described above.
The generic response and limits do not guarantee resistance to enumeration or
abuse, especially since signup currently reveals email conflicts. Accounts
without email, disabled accounts, and accounts unable to receive mail require
administrator assistance.

## Backups and restore checks

Take a PostgreSQL backup before upgrades and at least daily while the service
contains data. A local dump is only a short-term recovery copy: also keep
encrypted copies off the VPS, retain at least seven daily and four weekly
copies, and keep encryption credentials separate from the backups. Schedule
the dump command with a monitored daily job and alert on failures. Restrict
local backup access:

```sh
sudo install -d -o postgres -g postgres -m 0700 /var/backups/stillworld
sudo -u postgres pg_dump --format=custom \
  --file="/var/backups/stillworld/stillworld-$(date -u +%Y%m%dT%H%M%SZ).dump" \
  stillworld
```

Test backups by restoring to a separate, temporary database at least quarterly
and after a significant schema change. For example, with the dump path adjusted:

```sh
sudo -u postgres createdb --owner=stillworld stillworld_restore
sudo -u postgres pg_restore --no-owner --role=stillworld \
  --dbname=stillworld_restore \
  /var/backups/stillworld/stillworld-YYYYMMDDTHHMMSSZ.dump
```

Verify the restored schema and expected data before considering the backup
usable. Do not test a restore over the live database.

## Updating

Use a reviewed release or source revision and build the replacement binary
before the maintenance window. Back up the database, then:

```sh
sudo systemctl stop stillworld.service
sudo install -o root -g root -m 0755 ./stillworld /usr/local/bin/stillworld
sudo systemctl start stillworld-migrate.service
sudo systemctl start stillworld.service
sudo systemctl --no-pager --full status stillworld.service
curl --fail --silent --show-error https://play.example.net/healthz
```

`/healthz` confirms that the HTTP process responds; it is not a database
readiness or full application check. Review the service journal and exercise
the intended staging flows after each update. Schema migrations may not be
reversible: do not assume that reinstalling an older binary rolls back the
database. If an update fails after a schema change, stop the service and use a
verified database backup and a rehearsed restore procedure before resuming
traffic.
