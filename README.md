# dbTwin

**Instant, isolated MySQL copies for every pull request, powered by ZFS copy-on-write.**

Testing a migration on a tiny seed database tells you very little. Testing it on production is risky. dbtwin gives each pull request its own full copy of your real database in seconds. Run migrations, break things, throw it away. Production is never touched.

Copies are cheap because ZFS clones share blocks with the original. A new branch uses almost no disk until you start writing to it.

## How it works

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
3. Deletes `auto.cnf` so the branch gets its own MySQL server UUID
4. Starts a `mysql:8.4` container on the clone, using a free local port

If any step fails, dbtwin undoes what it already did (container, clone, snapshot).

`dbtwin delete` runs the same steps in reverse: stop the container, destroy the clone, destroy the snapshot.

## Requirements

- Linux
- ZFS (with a pool named `tank`)
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

The pool name (`tank`) and dataset names are hardcoded, and the snapshot is taken while main MySQL is running, so each branch starts from a crash-consistent copy that InnoDB recovers on startup. Expect breaking changes.

## Roadmap

- **GitHub App**: create a branch when a PR opens, delete it when the PR closes
- **Data masking**: hide sensitive data (emails, names, tokens) in branches
- **Binlog CDC sync**: keep main in sync with production through the binlog
- **Migration checks**: run migrations on the branch and report problems on the PR
- **Scale-to-zero**: stop idle branch containers and start them again on demand

## License

[Apache 2.0](LICENSE)
