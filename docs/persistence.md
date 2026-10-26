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
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:${PORT:-8080}:${PORT:-8080}"   # Main UI
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:${HAULER_UI_REGISTRY_PORT:-5000}:${HAULER_UI_REGISTRY_PORT:-5000}"   # Publishing front door
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:5002:5002"   # Serve page registry (default port)
      - "${HAULER_UI_BIND_ADDR:-127.0.0.1}:5001:5001"   # Serve page fileserver (default port)
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
│       ├── store/      # each haul's content store
│       ├── archives/   # saved and imported .tar.zst archives
│       └── extracted/  # default Extract output
├── imports/        # local files for Add File (see below)
├── store/          # HAULER_STORE_DIR - legacy single store
├── tmp/            # HAULER_TEMP_DIR - Temporary files
├── .docker/
│   └── config.json # Docker registry credentials
└── app.db          # SQLite database (jobs, settings, manifests)
```

## Folders hauler reads from and writes to

hauler runs as root in the container, so Wagon only passes it paths inside
these folders. Any other path gets HTTP 400 before hauler starts. Relative
paths are taken from the folder, and symlinks are resolved before the check.

| Operation | Field | Allowed folder |
|-----------|-------|----------------|
| Add File | `filePath` | an existing file in `/data/imports` |
| Add File | `url` | an `http://` or `https://` URL with a host (no bare paths, no `file://`). Wagon downloads it to a temporary file in `/data/imports` and hauler adds that file; hauler never sees the URL |
| Extract | `outputDir` | a folder inside the haul's folder, outside its `store`, `archives` and `registry` folders; default `/data/hauls/<slug>/extracted` |
| Copy Store | `dir://` target | a folder inside the haul's folder, outside its `store`, `archives` and `registry` folders |
| Serve | `directory` | a folder inside the haul's folder, outside its store |
| Serve | `tlsCert`, `tlsKey` | an existing file in `/data/certs` |

To add a local file, put it in `/data/imports` first (with the compose file,
that is `deploy/data/imports/` on the host), then enter its name, such as
`config.yaml`, under **Add File**. The Add File `name` must be a plain file
name: no `/` or `\`, and not `.` or `..`.

### Add File URL downloads

Wagon downloads an Add File `url` itself, because hauler's file getter opened
some `http://` URLs, such as `http://x/../../etc/passwd` or `http://app.db`,
as local paths. The download goes to a temporary file named
`.wagon-fetch-*` in `/data/imports`. hauler then runs
`hauler store add file <that file> --name <name>`, and Wagon deletes the
file when the job ends. Leftovers from a crash are deleted at the next start.

- Only `http` and `https`, for the URL and for every redirect (at most 10).
- Every connection is checked against the address it resolves to, including
  each redirect hop. Wagon checks the address before dialing and again on the
  socket itself, so a DNS answer cannot change in between.
- Always refused: loopback (`127.0.0.0/8`, `::1`), `0.0.0.0/8` and `::`,
  link-local (`169.254.0.0/16`, which includes the `169.254.169.254` cloud
  metadata address, and `fe80::/10`), multicast and reserved addresses, and
  the IPv4-mapped and NAT64 forms of these.
- Refused unless listed in `HAULER_UI_FETCH_ALLOW_CIDRS`: private ranges
  `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, CGNAT `100.64.0.0/10`
  (which includes Tailscale addresses) and IPv6 ULA `fc00::/7`. To fetch
  from a mirror on your LAN, list its range, for example
  `HAULER_UI_FETCH_ALLOW_CIDRS=10.20.0.0/16`.
- Wagon connects directly and ignores `HTTP_PROXY` and `HTTPS_PROXY` for
  these downloads, because a proxy would reach the target without the
  address check. If you can only reach the internet through a proxy, put
  the file in `/data/imports` and use **File Path** instead.
- A download is capped at `HAULER_UI_FETCH_MAX_BYTES` (default 2 GiB) and
  `HAULER_UI_FETCH_TIMEOUT` (default `30m`).
- The default `name` is the last part of the URL path (`tool.tgz` for
  `https://example.com/dist/tool.tgz`). When the path has none, for example
  `https://example.com/`, set `name`.
- A url job shows as running while it downloads. It does not wait for a free
  slot under `HAULER_UI_MAX_CONCURRENT_JOBS`, but it counts towards it.

### Registries and chart repositories

hauler, not Wagon, connects to image registries and chart repositories. Before
it creates the job, Wagon resolves these hosts and checks every address they
resolve to against the same rules as Add File urls:

- Add Image: the registry in `imageRef`, such as `registry.example.com:5000`
  in `registry.example.com:5000/app:1`. A reference without a registry, such
  as `nginx:1.27`, goes to Docker Hub and is not checked.
- Add Chart: the host of `repoUrl`, such as `https://charts.example.com` or
  `oci://registry.example.com/charts`, and the host of `name` when the chart
  name is a URL, such as `oci://ghcr.io/org/charts/app`. A `repoUrl` or URL
  `name` that is not a valid URL is refused, and so is one with a port but no
  host name, such as `http://:8080/x.tgz`. A value with no host, such as
  `grafana` or `bitnami/nginx`, is left to hauler. Wagon's parsing is close to
  helm's but not proven identical: a value the two read differently could
  still reach a host Wagon did not check.
- Sync: `registry` and `productRegistry`.

A blocked or unresolvable host gets HTTP 400 and no job. To use a registry or
chart repository on your LAN, list its range in `HAULER_UI_FETCH_ALLOW_CIDRS`.
hauler still uses `HTTP_PROXY` and `HTTPS_PROXY`, but the check runs in Wagon
with its own DNS: if Wagon cannot resolve a public registry name, the request
is refused even when the proxy could reach it.
hauler resolves the name again when the job runs, so this check does not stop
a DNS answer that changes in between. Image and chart references inside a Sync
manifest are not checked.

Only the first host is checked: the one in the request. Hosts that hauler or
helm reach after it are not: HTTP redirects (including a registry's blob
redirects), the token realm a registry names in its auth challenge, absolute
chart URLs in a repository's `index.yaml`, chart dependencies
(`addDependencies`) and images listed inside a chart (`addImages`).

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HAULER_DIR` | `/data` | Base directory for hauler data |
| `HAULER_STORE_DIR` | `/data/store` | Legacy single store. Each haul has its own store at `/data/hauls/<slug>/store` |
| `HAULER_TEMP_DIR` | `/data/tmp` | Temporary storage for operations like sync and extract |
| `DOCKER_CONFIG` | `/data/.docker` | Directory containing Docker registry authentication |
| `HOME` | `/data` | Set to `/data` so that `hauler login` writes credentials to the persistent volume |
| `HAULER_UI_FETCH_ALLOW_CIDRS` | (empty) | Private ranges an Add File url, image registry or chart repository may be on (see above) |
| `HAULER_UI_FETCH_MAX_BYTES` | `2147483648` | Max size of one Add File url download |
| `HAULER_UI_FETCH_TIMEOUT` | `30m` | Max time for one Add File url download |

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
