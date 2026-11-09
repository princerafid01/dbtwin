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
- Go 1.24+
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
  -e MYSQL_ROOT_PASSWORD=secret \
  -v /tank/mysql-main:/var/lib/mysql \
  -p 127.0.0.1:3306:3306 \
  mysql:8.4
```

Load your data into it. Every branch starts as a copy of this database and uses the same users and passwords.

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

| Setting            | Default           | Environment variable        |
|--------------------|-------------------|-----------------------------|
| `pool`             | `tank`            | `DBTWIN_POOL`               |
| `main_dataset`     | `tank/mysql-main` | `DBTWIN_MAIN_DATASET`       |
| `branch_prefix`    | `tank/mysql-`     | `DBTWIN_BRANCH_PREFIX`      |
| `container_prefix` | `mysql-`          | `DBTWIN_CONTAINER_PREFIX`   |
| `mysql_image`      | `mysql:8.4`       | `DBTWIN_MYSQL_IMAGE`        |
| `bind_address`     | `127.0.0.1`       | `DBTWIN_BIND_ADDRESS`       |

Example `/etc/dbtwin/config.json`:

```json
{
  "pool": "data",
  "main_dataset": "data/mysql-main",
  "branch_prefix": "data/mysql-",
  "mysql_image": "mysql:8.0"
}
```

If you change `pool`, also set `main_dataset` and `branch_prefix` to match.

Override one setting for a single command:

```bash
sudo DBTWIN_MYSQL_IMAGE=mysql:9.1 dbtwin create pr-42
```

See the settings in use:

```bash
sudo dbtwin config
```

The output is JSON, so you can save it as a starting config file. A misspelled key or an empty value stops dbtwin with a clear error.

## Usage

If you built from source, use `./dbtwin` instead of `dbtwin`.

### Create a branch

```console
$ sudo dbtwin create pr-42
> zfs [snapshot tank/mysql-main@pr-42]
> zfs [clone tank/mysql-main@pr-42 tank/mysql-pr-42]
> docker [run -d --name mysql-pr-42 -v /tank/mysql-pr-42:/var/lib/mysql -p 127.0.0.1:41235:3306 mysql:8.4]
branch pr-42 ready on 127.0.0.1:41235
```

Connect to it like any MySQL server:

```bash
mysql -h 127.0.0.1 -P 41235 -u root -p
```

### List branches

```console
$ sudo dbtwin list
BRANCH       USED     PORT
pr-42        1.2M     127.0.0.1:41235
pr-43        840K     127.0.0.1:38017
```

`USED` is only the data that changed compared to main.

### Delete a branch

```console
$ sudo dbtwin delete pr-42
> docker [rm -f mysql-pr-42]
> zfs [destroy tank/mysql-pr-42]
> zfs [destroy tank/mysql-main@pr-42]
```

## Status

⚠️ **Early prototype. Not production-ready.**

- The snapshot is taken while main MySQL is running, so each branch starts from a crash-consistent copy that InnoDB recovers on startup.
- Main is a MySQL you load yourself. Syncing it from production is on the roadmap.
- Expect breaking changes.

## Roadmap

- **GitHub App**: create a branch when a PR opens, delete it when the PR closes
- **Data masking**: hide sensitive data (emails, names, tokens) in branches
- **Production sync**: refresh main from production with dumps, then binlog CDC
- **Migration checks**: run migrations on the branch and report problems on the PR
- **Scale-to-zero**: stop idle branch containers and start them again on demand

## License

[Apache 2.0](LICENSE)
