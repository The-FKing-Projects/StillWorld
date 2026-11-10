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

These are per-process, in-memory limits, not shared across servers, and restart
with the process. Successful login clears the username failure counter. The
password-hash work uses Argon2id with 64 MiB per operation by default, so four
simultaneous operations can consume about 256 MiB for hashing alone, in
addition to PostgreSQL, the application, and the operating system. Load-test
the target host and monitor resource use; do not treat these initial limits as
a guarantee of public-service capacity or abuse resistance.

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
