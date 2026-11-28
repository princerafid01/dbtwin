# dbtwin

**Instant, isolated MySQL copies for every pull request, powered by ZFS copy-on-write.**

Testing a migration on a tiny seed database tells you very little. Testing it on production is risky. dbtwin gives each pull request its own full copy of your main database in seconds. Run migrations, break things, throw it away. Your main database is never touched.

## Why ZFS?

ZFS snapshots are instant, and clones share blocks with the original. A new branch copies no data: it uses almost no disk until you start writing to it, and each branch only stores its own changes.

## How it works

The examples in this README use the default settings: pool `tank`, main dataset `tank/mysql-main`, image `mysql:8.4`. You can change all of them (see [Configuration](#configuration)).

```
  tank/mysql-main            (your main MySQL data)
        │
        │  zfs snapshot
        ▼
  tank/mysql-main@pr-42      (read-only point-in-time copy)
        │
        │  zfs clone
        ▼
  tank/mysql-pr-42           (writable copy, shares blocks with main)
        │
        │  docker run mysql:8.4
        ▼
  container mysql-pr-42  →  127.0.0.1:<free port>
```

When you run `dbtwin create pr-42`, it:

1. Snapshots `tank/mysql-main` as `tank/mysql-main@pr-42`
2. Clones the snapshot to `tank/mysql-pr-42`
3. Asks ZFS where the clone is mounted
4. Deletes `auto.cnf` so the branch gets its own MySQL server UUID
5. Starts a `mysql:8.4` container on the clone, using a free local port

If any step fails, dbtwin undoes what it already did (container, clone, snapshot).

`dbtwin delete` runs the same steps in reverse: stop the container, destroy the clone, destroy the snapshot.

## Requirements

- Linux
- ZFS (a pool named `tank` by default; configurable)
- Docker
- Go 1.26+
- Root access (for `zfs` and `docker`)

## Quick start

**1. Create the ZFS dataset for your main database.** These settings work well for InnoDB:

```bash
sudo zfs create -o recordsize=16K -o compression=lz4 -o atime=off tank/mysql-main
```

**2. Run your main MySQL container on that dataset:**

```bash
sudo docker run -d \
  --name mysql-main \
  --restart unless-stopped \
  -e MYSQL_ROOT_PASSWORD=secret \
  -v /tank/mysql-main:/var/lib/mysql \
  -p 127.0.0.1:3306:3306 \
  mysql:8.4
```

Every branch starts as a copy of this database and uses the same users and passwords. Fill it with `dbtwin refresh` (see [Refresh main](#refresh-main)), or load your data into it yourself.

Keep `MYSQL_ROOT_PASSWORD` the same as root's real password: `dbtwin refresh` logs in to main with it.

**3. Install dbtwin:**

```bash
go install github.com/princerafid01/dbtwin@latest
```

This puts the binary in `$(go env GOPATH)/bin`. `sudo` usually doesn't search that directory, so copy it somewhere it does:

```bash
sudo cp "$(go env GOPATH)/bin/dbtwin" /usr/local/bin/
```

Or build from source:

```bash
git clone https://github.com/princerafid01/dbtwin.git
cd dbtwin
go build -o dbtwin
```

## Configuration

dbtwin works with no configuration. To change a setting, use a config file, environment variables, or both.

Settings are loaded in this order (later wins):

1. Built-in defaults
2. The config file `/etc/dbtwin/config.json`, if it exists
3. Environment variables

| Setting              | Default           | Environment variable        |
|----------------------|-------------------|-----------------------------|
| `pool`               | `tank`            | `DBTWIN_POOL`               |
| `main_dataset`       | `tank/mysql-main` | `DBTWIN_MAIN_DATASET`       |
| `main_container`     | `mysql-main`      | `DBTWIN_MAIN_CONTAINER`     |
| `branch_prefix`      | `tank/mysql-`     | `DBTWIN_BRANCH_PREFIX`      |
| `container_prefix`   | `mysql-`          | `DBTWIN_CONTAINER_PREFIX`   |
| `mysql_image`        | `mysql:8.4`       | `DBTWIN_MYSQL_IMAGE`        |
| `bind_address`       | `127.0.0.1`       | `DBTWIN_BIND_ADDRESS`       |
| `branch_memory`      | `512m`            | `DBTWIN_BRANCH_MEMORY`      |
| `branch_buffer_pool` | `128M`            | `DBTWIN_BRANCH_BUFFER_POOL` |
| `source_host`        | (none)            | `DBTWIN_SOURCE_HOST`        |
| `source_port`        | `3306`            | `DBTWIN_SOURCE_PORT`        |
| `source_user`        | (none)            | `DBTWIN_SOURCE_USER`        |
| `source_password`    | (none)            | `DBTWIN_SOURCE_PASSWORD`    |
| `source_databases`   | (none)            | `DBTWIN_SOURCE_DATABASES`   |
| `mask_secret`        | (none)            | `DBTWIN_MASK_SECRET`        |
| `mask`               | (none)            | (config file only)          |
| `api_address`        | `127.0.0.1:8080`  | `DBTWIN_API_ADDRESS`        |
| `api_token`          | (none)            | `DBTWIN_API_TOKEN`          |
| `api_domain`         | (none)            | `DBTWIN_API_DOMAIN`         |

Example `/etc/dbtwin/config.json`:

```json
{
  "pool": "data",
  "main_dataset": "data/mysql-main",
  "branch_prefix": "data/mysql-",
  "mysql_image": "mysql:8.0"
}
```

`branch_memory` is the Docker memory limit for each branch container (`docker run --memory`). `branch_buffer_pool` is the InnoDB buffer pool size for each branch; keep it well below `branch_memory`. Branch containers also run with the performance schema off to save RAM. These limits apply only to branches, not to your main MySQL container.

The `source_*` settings describe the MySQL server that dbtwin copies from. They may stay empty until you use it. The easiest way to set them is `dbtwin connect` (see [Connect your database](#connect-your-database)). `source_databases` is a comma-separated list, e.g. `"shop,blog"`. `dbtwin config` shows `source_password` as `***`.

`main_container` is the name of your main MySQL container. Only `dbtwin refresh` uses it.

`mask` and `mask_secret` hide private data during `dbtwin refresh` (see [Mask private data](#mask-private-data)). `dbtwin config` shows `mask_secret` as `***`.

`api_address`, `api_token` and `api_domain` are for `dbtwin serve` (see [HTTP API](#http-api)). `dbtwin config` shows `api_token` as `***`.

If you change `pool`, also set `main_dataset` and `branch_prefix` to match.

Override one setting for a single command:

```bash
sudo DBTWIN_MYSQL_IMAGE=mysql:9.1 dbtwin create pr-42
```

See the settings in use:

```bash
sudo dbtwin config
```

The output is JSON, so you can save it as a starting config file (put the real `source_password` and `mask_secret` back in). A misspelled key or an empty value stops dbtwin with a clear error.

## Connect your database

Tell dbtwin which MySQL server to copy from, the "source". `dbtwin connect` tests the connection and saves the settings only if the test passes.

```console
$ sudo dbtwin connect --host db.example.com --user dbtwin --databases shop,blog
source password:
connected to db.example.com:3306 as dbtwin
DATABASE                  SIZE (MB)
shop                         812.4
blog                          35.0

saved source settings:
  host:       db.example.com
  port:       3306
  user:       dbtwin
  password:   ***
  databases:  shop,blog
Run "dbtwin connect --test" to test them again later.
```

| Flag          | Required | Default | Meaning                                   |
|---------------|----------|---------|-------------------------------------------|
| `--host`      | yes      |         | host of the source MySQL server           |
| `--port`      | no       | `3306`  | port of the source MySQL server           |
| `--user`      | yes      |         | MySQL user to read the source with        |
| `--databases` | yes      |         | comma-separated list, e.g. `shop,blog`    |

The password is asked for with a hidden prompt, so it never appears on screen or in your shell history. In scripts, set `DBTWIN_SOURCE_PASSWORD` instead and nothing is asked.

If the test fails, nothing is saved and you see why. If it passes, the `source_*` settings are written to `/etc/dbtwin/config.json`:

- your other settings in the file are kept
- `/etc/dbtwin` is created if it doesn't exist
- the file gets permissions `0600`, so only root can read it, because it contains the password
- the file is replaced in one step, so a crash never leaves a half-written config

`connect` saves exactly what you give it: a missing `--port` means `3306`, even if `DBTWIN_SOURCE_PORT` is set. The other `DBTWIN_SOURCE_*` variables are ignored too, except `DBTWIN_SOURCE_PASSWORD`.

### Test the saved source

```console
$ sudo dbtwin connect --test
connected to db.example.com:3306 as dbtwin
DATABASE                  SIZE (MB)
shop                         812.4
blog                          35.0
```

`connect --test` uses the saved settings (the config file, then `DBTWIN_SOURCE_*` environment variables) and changes nothing. Run it any time, for example after the source password changes.

Both commands connect with a throwaway `mysql_image` container (`docker run --rm --network host`), so you don't need a MySQL client on the host. The password is passed only through the `MYSQL_PWD` environment variable, never on a command line. Sizes are data plus indexes, from `information_schema.tables`.

`source_databases` can't include MySQL's own databases (`mysql`, `sys`, `information_schema`, `performance_schema`).

## Refresh main

`dbtwin refresh` replaces the data in main with a fresh copy of the source databases.

```console
$ sudo dbtwin refresh
connected to db.example.com:3306 as dbtwin
DATABASE                  SIZE (MB)
shop                         812.4
blog                          35.0
> zfs [snapshot tank/mysql-main@before-refresh]
dropping the old copies in main: shop, blog
copied 790 MB
> zfs [destroy tank/mysql-main@before-refresh]
main now has a fresh copy of: shop, blog
```

What it does:

1. **Checks** the source connection, the main container, the root login to main and the free disk space. If a check fails, nothing is changed.
2. **Takes a backup**: the snapshot `main@before-refresh`.
3. **Drops** each source database in main, so tables that were removed on the source don't stay behind. Other databases in main are not touched.
4. **Copies** the data with `mysqldump`, streamed straight into main's `mysql`. Nothing is written to a file on disk.
5. **Masks** private data, if you set up [mask rules](#mask-private-data).
6. **Deletes the backup** when the copy is complete.

Existing branches don't change. They keep the data they were created with. Branches created after the refresh get the new data.

### Safety

- **Production is only read.** `mysqldump` runs with `--single-transaction`, so InnoDB tables are copied consistently and without locks. MyISAM tables are not protected by this and can be copied in an inconsistent state.
- **A failed refresh is undone.** If any step after the backup fails, dbtwin stops main's container, rolls main back to the backup with `zfs rollback`, and starts it again. Main is then exactly as it was before the refresh. This covers a failed dump or load, and a dump that stopped before its last line (`-- Dump completed`).
- **Ctrl+C is safe.** It stops the copy and rolls main back. Don't press it again while the rollback runs.
- **One command at a time.** While a refresh runs, `create`, `delete` and a second `refresh` stop with "another dbtwin command is running".
- **After a crash** (power loss, `kill -9`), the backup snapshot stays behind and main may be half-loaded. The next refresh stops and shows the commands to roll main back by hand.

### Requirements

- **The main container must be running**, with its data in `main_dataset`. dbtwin checks both.
- **Root's password in main must match `MYSQL_ROOT_PASSWORD`.** dbtwin logs in to main with the container's own `MYSQL_ROOT_PASSWORD`, so it never needs the password itself. MySQL reads that variable only when the container is first set up. If root's password was changed later, the login fails: recreate the container with the current password.
- **Free disk space at least the size of the source databases.** The old data stays in the backup until the copy is complete. dbtwin checks this before it starts. The source size is a careful guess: compression usually makes the copy smaller.
- **The source user needs** `SELECT`, `SHOW VIEW`, `TRIGGER`, `EVENT` and `SHOW_ROUTINE` (MySQL 8.0.20 and later) on the source databases. dbtwin dumps tables, views, triggers, events, stored procedures and functions. It doesn't need `PROCESS` or `RELOAD`.
- Sources with GTID turned on (for example Amazon RDS) work: the dump leaves out the GTID line.

### Refresh every night

To have fresh data in main every morning, let cron run the refresh at night.

**1. Install dbtwin where cron can find it:**

```bash
sudo cp dbtwin /usr/local/bin/
```

**2. Create `/etc/cron.d/dbtwin`:**

```
PATH=/usr/sbin:/usr/bin:/bin
0 3 * * * root (date; /usr/local/bin/dbtwin refresh) >> /var/log/dbtwin-refresh.log 2>&1
```

- `PATH` adds `/usr/sbin`, which cron's default leaves out. On some systems `zfs` is only there, and without this line every refresh fails with `executable file not found in $PATH`.
- `root` because refresh needs root for `zfs` and `docker`.
- The log is the only place to see what happened, since cron has no screen. `date` adds the time of each run.
- `0 3 * * *` means 03:00 every day, in the server's time zone.

**3. Check it the next morning:**

```bash
sudo dbtwin list                       # (main) DATA FROM should be from last night
sudo tail -20 /var/log/dbtwin-refresh.log
```

If main's `DATA FROM` is older than last night, the refresh failed. The log says why. Common reasons:

- **Another command was running.** A `create`, `delete` or `refresh` running at 03:00 makes the refresh stop with "another dbtwin command is running". Nothing changes, and it runs again the next night.
- **A backup was left behind by a crash.** Every later refresh stops until you fix main by hand. The log shows the commands (see [Safety](#safety)).
- **The source can't be reached** or its password changed. Test it with `dbtwin connect --test`.

### Mask private data

Refresh can hide private data, such as emails, names and phone numbers, so it never stays in main or in any branch. Add rules to `/etc/dbtwin/config.json`:

```json
{
  "mask_secret": "a-long-random-secret",
  "mask": {
    "shop.customers": { "email": "email", "name": "hash", "phone": "null" },
    "shop.orders":    { "note": "fixed:hidden" }
  }
}
```

Each key in `mask` is `database.table`. Inside it, each column gets one rule:

| Rule           | New value                                       | Example                                  |
|----------------|-------------------------------------------------|------------------------------------------|
| `email`        | a fake email, 28 characters                     | `98b37f66a187e6a2@example.com`           |
| `hash`         | 16 random-looking characters                    | `575ad1bb23d81c14`                       |
| `null`         | empty (`NULL`)                                  | `NULL`                                   |
| `fixed:<text>` | the same text in every row                      | `fixed:hidden` gives `hidden`            |

Columns without a rule are copied as they are.

```console
$ sudo dbtwin refresh
...
copied 790 MB
masking shop.customers
masking shop.orders
main now has a fresh copy of: shop
```

- **The same value always gets the same fake value.** `ayesha@gmail.com` becomes the same fake email in every table (with the same rule) and on every refresh, so joins between tables still match. The fake values are made from the real ones and `mask_secret`: without the secret, nobody can work back to the real data.
- **Keep `mask_secret` secret, and don't change it.** A new secret gives every row a new fake value. You can also set it with `DBTWIN_MASK_SECRET` instead of the file.
- **A mistake stops the refresh.** An unknown rule or a table not written as `database.table` stops refresh before anything changes. A column that doesn't exist, or a fake value that doesn't fit its column, stops it after the copy, and main is rolled back. Main never keeps unmasked data.
- **Short columns:** `email` needs room for 28 characters and `hash` for 16. For shorter columns, use `null` or `fixed:<text>`. `hash` and `email` are for text columns; use `null` or `fixed:<text>` for number and date columns.
- **The source is never changed.** Masking runs on main, after the copy.
- Masking rewrites each masked table once more after the copy, so very large masked tables make refresh take longer.

## Usage

If you built from source, use `./dbtwin` instead of `dbtwin`.

### Create a branch

```console
$ sudo dbtwin create pr-42
> zfs [snapshot tank/mysql-main@pr-42]
> zfs [clone tank/mysql-main@pr-42 tank/mysql-pr-42]
> docker [run -d --restart unless-stopped --name mysql-pr-42 -v /tank/mysql-pr-42:/var/lib/mysql -p 127.0.0.1:41235:3306 mysql:8.4]
waiting for MySQL...
branch pr-42 is ready
  host:     127.0.0.1
  port:     41235
  user:     root (same users and passwords as main)
  connect:  mysql -h 127.0.0.1 -P 41235 -u root -p
```

Connect to it like any MySQL server:

```bash
mysql -h 127.0.0.1 -P 41235 -u root -p
```

### List branches

```console
$ sudo dbtwin list
BRANCH       USED     PORT             DATA FROM
(main)       10.9M    127.0.0.1:3306   2026-11-24 02:43
pr-42        1.2M     127.0.0.1:41235  2026-11-24 02:43
pr-43        840K     127.0.0.1:38017  2026-11-20 03:00
```

`USED` for a branch is only the data that changed compared to main.

`DATA FROM` is when the data was copied from the source by `dbtwin refresh`. A branch keeps the date main had when the branch was created, so later refreshes don't change it. `unknown` means it was never refreshed.

### Delete a branch

```console
$ sudo dbtwin delete pr-42
> docker [rm -f mysql-pr-42]
> zfs [destroy tank/mysql-pr-42]
> zfs [destroy tank/mysql-main@pr-42]
```

### Reset a branch

`dbtwin reset` gives a branch main's newest data. It keeps the branch's name and port, so apps connected to it keep working.

```console
$ sudo dbtwin reset pr-42
> docker [rm -f mysql-pr-42]
> zfs [destroy tank/mysql-pr-42]
> zfs [destroy tank/mysql-main@pr-42]
> zfs [snapshot -o dbtwin:data-from=2026-11-24T03:00:00+01:00 tank/mysql-main@pr-42]
> zfs [clone tank/mysql-main@pr-42 tank/mysql-pr-42]
> docker [run -d --restart unless-stopped --name mysql-pr-42 -v /tank/mysql-pr-42:/var/lib/mysql -p 127.0.0.1:41235:3306 ...]
waiting for MySQL...
branch pr-42 is ready
  host:     127.0.0.1
  port:     41235
  ...
```

⚠️ **Everything changed on the branch is lost.** Reset is `delete` followed by `create`, so there is no undo.

- The branch gets main's data as it is now. To get the newest data from the source, run `dbtwin refresh` first.
- If another program took the branch's port in the meantime, reset picks a new port and prints a warning.
- If the branch can't be made again, the old one is already deleted. Run `dbtwin create <name>` to make it again.

## HTTP API

`dbtwin serve` runs an HTTP API, so CI (for example a GitHub Action) can create, reset and delete branches from another machine. It does the same work as the commands above, with the same checks and the same lock.

### Start the API

Make a secret token and put it in `/etc/dbtwin/config.json`:

```bash
openssl rand -hex 32
```

```json
{
  "api_token": "paste-the-token-here"
}
```

Then start it:

```console
$ sudo dbtwin serve
2026/11/27 19:26:13 dbtwin API listening on 127.0.0.1:8080
```

`dbtwin serve` refuses to start without `api_token`. It listens on `127.0.0.1:8080` (`api_address`), so only this machine can reach it until you add HTTPS (see [Reach the API from outside](#reach-the-api-from-outside)).

### Keep it running

[contrib/dbtwin.service](contrib/dbtwin.service) lets systemd start the API at boot, restart it if it crashes, and stop it safely:

```bash
sudo cp dbtwin /usr/local/bin/
sudo cp contrib/dbtwin.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now dbtwin
journalctl -u dbtwin -f    # see the log
```

When the API stops (`systemctl stop` or Ctrl+C), it takes no new requests and waits up to 2 minutes for a running create, reset or delete to finish. Press Ctrl+C twice to stop at once.

### Reach the API from outside

The token must never travel over plain HTTP, so use HTTPS. Pick one:

| You have                     | Do this                                                                                                          |
|------------------------------|------------------------------------------------------------------------------------------------------------------|
| Nothing on port 443          | Set `"api_domain": "dbtwin.example.com"`. dbtwin serves HTTPS on port 443 with free Let's Encrypt certificates. |
| Caddy                        | Use [contrib/Caddyfile](contrib/Caddyfile).                                                                      |
| nginx                        | Use [contrib/nginx.conf](contrib/nginx.conf) and `certbot --nginx`.                                              |
| CI on this same machine      | Nothing. Call `http://127.0.0.1:8080`.                                                                           |

With `api_domain`, the domain must point to this server and port 443 must be open. Certificates are kept in `/var/lib/dbtwin/certs`. If another program already uses port 443, `dbtwin serve` says so and stops; leave `api_domain` empty and use Caddy or nginx instead.

### Routes

Every `/v1` request needs the header `Authorization: Bearer <token>`.

| Method   | Path                          | Does                                            | Success |
|----------|-------------------------------|-------------------------------------------------|---------|
| `GET`    | `/healthz`                    | Is the API up? (no token needed)                | `200`   |
| `GET`    | `/v1/branches`                | List main and every branch                      | `200`   |
| `GET`    | `/v1/branches/{name}`         | One branch                                      | `200`   |
| `POST`   | `/v1/branches`                | Create a branch, body `{"name":"pr-42"}`        | `201`   |
| `POST`   | `/v1/branches/{name}/reset`   | Reset a branch to main's newest data            | `200`   |
| `DELETE` | `/v1/branches/{name}`         | Delete a branch                                 | `200`   |

Create a branch:

```console
$ curl -s -H "Authorization: Bearer $TOKEN" -d '{"name":"pr-42"}' http://127.0.0.1:8080/v1/branches
{"name":"pr-42","main":false,"host":"127.0.0.1","port":41235,"status":"running","data_from":"2026-11-25T02:57:45+01:00"}
```

List branches:

```console
$ curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/v1/branches
{"branches":[{"name":"main","main":true,"host":"127.0.0.1","port":3306,"status":"running","used":"11.4M","data_from":"2026-11-25T02:57:45+01:00"},{"name":"pr-42","main":false,"host":"127.0.0.1","port":41235,"status":"running","used":"45K","data_from":"2026-11-25T02:57:45+01:00"}]}
```

Reset and delete:

```bash
curl -s -X POST   -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/v1/branches/pr-42/reset
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/v1/branches/pr-42
```

A create or reset can take a while (MySQL has to start). If the client gives up or disconnects, the work still finishes; ask again with `GET /v1/branches/{name}`.

### Errors

Errors look like this:

```json
{"error":{"code":"not_found","message":"branch pr-42 not found (see \"dbtwin list\")"}}
```

| Status | Code             | Means                                                                    |
|--------|------------------|--------------------------------------------------------------------------|
| `400`  | `bad_request`    | The body isn't JSON like `{"name":"pr-42"}`, or is over 1 KB             |
| `400`  | `invalid_name`   | Names use only letters, numbers, `-` and `_` (max 40 characters)         |
| `400`  | `main_protected` | `main` can't be created, reset or deleted                                |
| `401`  | `unauthorized`   | Missing or wrong token                                                   |
| `404`  | `not_found`      | No such branch, or no such route                                         |
| `409`  | `already_exists` | A branch with this name already exists                                   |
| `409`  | `busy`           | Another create, reset, delete or refresh is running. Try again after `Retry-After` seconds |
| `503`  | `not_ready`      | ZFS, Docker or main is not available on the server                       |
| `500`  | `internal`       | Something else failed; the server log has the details                   |

### Log

The API writes one line per request, without the token:

```
2026/11/27 07:05:41 POST /v1/branches 201 14.2s
2026/11/27 07:05:42 DELETE /v1/branches/main 400 181ms
```

### Tests

The API has tests that need no root, ZFS or Docker:

```bash
go test ./...
```

## Status

⚠️ **Early prototype. Not production-ready.**

- The snapshot is taken while main MySQL is running, so each branch starts from a crash-consistent copy that InnoDB recovers on startup.
- `dbtwin refresh` copies everything each time with a full dump. Large databases take a while.
- Expect breaking changes.

## Roadmap

- **GitHub App**: create a branch when a PR opens, delete it when the PR closes
- **Production sync**: keep main up to date from the binlog (CDC) instead of full dumps
- **Migration checks**: run migrations on the branch and report problems on the PR
- **Scale-to-zero**: stop idle branch containers and start them again on demand

## License

[Apache 2.0](LICENSE)
