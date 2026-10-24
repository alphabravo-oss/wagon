# Container Persistence Model

## Overview

Wagon uses `/data` as the persistent root directory for all stored data. This directory must be mounted as a volume to ensure data persists across container restarts.

## Why /data Must Be Mounted

All application state is stored within `/data`:
- **Store content** - Downloaded images, charts, and files
- **Registry credentials** - Docker config.json with auth tokens
- **Database** - Job history, settings, and saved manifests
- **Temporary files** - Working space during sync/save/load operations

Without a volume mount, **all data is lost** when the container is removed or restarted.

## Required Volume Mount

The container expects a volume mount at `/data`:

```bash
docker run -v wagon-data:/data -p 127.0.0.1:8080:8080 -e HAULER_UI_PASSWORD=your-strong-password wagon:local
```

Or in docker-compose.yml:

```yaml
services:
  wagon:
    build: ..
    ports:
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:${PORT:-8080}:8080"   # Main UI
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:5000:5000"            # Registry serve
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:5001:5001"            # Fileserver serve
    volumes:
      - ./data:/data
    env_file:                # every setting in deploy/.env reaches the container
      - path: .env
        required: false
    environment:
      - PORT=${PORT:-8080}
      - HAULER_UI_PASSWORD=${HAULER_UI_PASSWORD:-}
      - HAULER_LOG_LEVEL=${HAULER_LOG_LEVEL:-info}
      - HAULER_IGNORE_ERRORS=${HAULER_IGNORE_ERRORS:-false}
      - HAULER_RETRIES=${HAULER_RETRIES:-0}
```

> **Note**: The authoritative docker-compose configuration is in `deploy/docker-compose.yml` with environment variable defaults in `deploy/.env.example`. Compose bind-mounts `deploy/data/`, not a named volume.

## Directory Structure

Within `/data`, the following structure is created:

```
/data/
├── hauls/
│   └── <slug>/
│       └── store/  # each haul's content store
├── store/          # HAULER_STORE_DIR - legacy single store
├── tmp/            # HAULER_TEMP_DIR - Temporary files
├── .docker/
│   └── config.json # Docker registry credentials
└── app.db          # SQLite database (jobs, settings, manifests)
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HAULER_DIR` | `/data` | Base directory for hauler data |
| `HAULER_STORE_DIR` | `/data/store` | Legacy single store. Each haul has its own store at `/data/hauls/<slug>/store` |
| `HAULER_TEMP_DIR` | `/data/tmp` | Temporary storage for operations like sync and extract |
| `DOCKER_CONFIG` | `/data/.docker` | Directory containing Docker registry authentication |
| `HOME` | `/data` | Set to `/data` so that `hauler login` writes credentials to the persistent volume |

For the complete list of environment variables, see `deploy/.env.example`.

## Registry Login Storage

### How It Works

When you run `hauler login <registry>` (via CLI or UI), credentials are stored following the standard Docker auth pattern:

- **Container path**: `/data/.docker/config.json` (or `~/.docker/config.json` which resolves to the same location since `HOME=/data`)
- **Format**: Standard Docker config.json. The auth entries are **base64-encoded, not encrypted**

### Login Flow via UI

1. User navigates to "Registry Login" page
2. Enters registry URL, username, and password
3. Backend creates an async job running `hauler login`
4. Credentials are passed via environment variables (not CLI args) for security
5. Hauler writes credentials to `/data/.docker/config.json`
6. Credentials persist for future operations (sync, copy, etc.)

### Viewing Stored Credentials

```bash
docker exec wagon cat /data/.docker/config.json
```

### Clearing Credentials

```bash
# Remove the config file
docker exec wagon rm /data/.docker/config.json

# Or logout via UI or CLI
docker exec wagon hauler logout registry.example.com
```

### Credential Security

- **In UI**: Passwords are never stored in the SQLite database
- **In transit**: Credentials passed via environment variables to child process
- **At rest**: Stored in Docker config.json format, **base64-encoded, not encrypted**. Anyone who can read the `/data` volume can recover the passwords, so restrict access to the volume and its backups
- **Logs**: Job logs redact sensitive information

### Multiple Registries

You can be logged into multiple registries simultaneously. Each login adds an entry to the config.json:

```json
{
  "auths": {
    "registry.example.com": {
      "auth": "base64(username:password)"
    },
    "ghcr.io": {
      "auth": "base64(user:token)"
    }
  }
}
```

## Permissions

The container creates directories with `755` permissions. Ensure the host mount directory is writable by the container user (typically root or the user specified in the image).

## Migration Policy

Schema changes are applied by numbered SQL migrations in
`backend/internal/sqlite/migrations/`. Each migration is recorded as applied by
its version number and will not re-run. This has strict consequences:

1. **Migrations are immutable once released.** Never edit the body of a
   migration that has already shipped. Because an applied migration will not
   re-run, editing it changes only fresh installs and silently diverges them
   from already-upgraded installs. Fix or evolve the schema with a *new*
   migration instead.

2. **All NEW migrations must be additive and non-destructive.** Prefer
   `ALTER TABLE` / `ADD COLUMN`. When a table must be reshaped, do it safely:
   create the new table, backfill existing rows into it, then swap. Never
   `DROP` a table (or column) that holds user data.

3. **Migrations 0005 and 0006 contain legacy destructive DROPs.** They
   `DROP TABLE IF EXISTS store_contents;` and
   `DROP TABLE IF EXISTS saved_manifests;` respectively, recreating each table
   with a new `haul_id` column. These date from the alpha period and would
   destroy pre-existing data on an upgrade (a fresh install is unaffected
   because the tables are empty). **They are the last permitted destructive
   migrations.** Every migration from 0007 onward must follow rule 2.

4. **Follow-up: CI migration test on a populated DB.** A test that applies the
   full migration chain against a database seeded with representative user data
   — and asserts that no rows are lost — is planned. See
   `docs/enterprise-readiness.md` section 2.5.

## Storage Requirements

- **Store size**: Proportional to content added (images, charts, files)
- **Temp size**: Temporary space during sync/save/load operations
- **Docker config**: Negligible (a few KB)

Plan host volume space accordingly based on your intended usage.
