# Wagon Runbook

## Overview

Wagon is a single-container web interface for the Rancher Government Hauler CLI. It provides full operational parity with Hauler's command-line interface including store management, registry operations, and artifact synchronization.

**Port**: 8080 (HTTP)
**Persistent Data**: `/data` (required volume mount)

## Quick Start

Install, backup and upgrade steps live in the [README](../README.md#quick-start).
In short: build the image from source (`docker build -t wagon:local .`), then
run it with `HAULER_UI_PASSWORD` set and a named volume on `/data`. Publish
`5000` as well if you use Publishing. `ghcr.io/alphabravo-oss/wagon:latest`
predates the current secure defaults until a new release is cut, so don't use
it for now.

## Building from Source

### Prerequisites

- Go 1.26+
- Node.js 22+
- Docker (for container build)

### Build Steps

```bash
# Clone the repository
git clone https://github.com/alphabravo-oss/wagon.git
cd wagon

# Using Make (recommended)
make build

# Or manually
cd backend && go build -o server .
cd ../web && npm install && npm run build
```

### Build Docker Image

```bash
# Using Make
make docker-build

# Or manually
docker build -t wagon:latest .
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP port for the UI |
| `HAULER_UI_PASSWORD` | (none, **required**) | Password for UI/API access; Wagon refuses to start without it |
| `HAULER_UI_BIND_ADDR` | `127.0.0.1` | Compose only: host address ports are published on; change deliberately to expose Wagon |
| `HAULER_UI_TRUSTED_ORIGINS` | (empty) | Extra origins allowed to make changes (reverse proxy that rewrites `Host`) |
| `HAULER_UI_INSECURE_NO_AUTH` | `false` | **Danger.** `true` starts with no authentication. Local throwaway testing only |
| `HAULER_UI_FETCH_ALLOW_CIDRS` | (empty) | Comma-separated private ranges an Add File url may reach, such as `10.20.0.0/16`. Without it, RFC 1918, CGNAT `100.64.0.0/10` and IPv6 `fc00::/7` are refused. Loopback, link-local (including `169.254.169.254`), multicast and reserved addresses are always refused |
| `HAULER_UI_FETCH_MAX_BYTES` | `2147483648` (2 GiB) | Max size of one Add File url download |
| `HAULER_UI_FETCH_TIMEOUT` | `30m` | Max time for one Add File url download, redirects included (Go duration) |
| `HAULER_LOG_LEVEL` | `info` | Hauler CLI log level (debug, info, warn, error) |
| `HAULER_IGNORE_ERRORS` | `false` | Continue operations despite errors |
| `HAULER_RETRIES` | `0` | Number of retries for failed operations (also settable in the Settings UI) |
| `HAULER_DIR` | `/data` | Base directory for hauler data |
| `HAULER_STORE_DIR` | `/data/store` | Legacy single store. Each haul has its own store at `/data/hauls/<slug>/store` |
| `HAULER_TEMP_DIR` | `/data/tmp` | Temporary files location |
| `DOCKER_CONFIG` | `/data/.docker` | Docker auth config directory |
| `DATABASE_PATH` | `/data/app.db` | SQLite database path |

**Optional variables** (not in .env.example but supported):
- `HAULER_DEFAULT_PLATFORM` - Default platform for multi-arch operations
- `HAULER_KEY_PATH` - Path to signing key for file verification

> **Source of truth**: See `deploy/.env.example` for the complete list of documented environment variables.

### Volume Mounts

The `/data` directory is **required** for persistence:

```bash
-v wagon-data:/data
```

Without this mount, all data (haul stores, registry credentials, settings) is lost when the container is removed.

## Accessing the UI

1. Open http://localhost:8080 in your browser
2. Enter the `HAULER_UI_PASSWORD` password
3. The dashboard shows available operations and current job status

## Common Workflows

### Main pages

- **Hauls**: create and switch between hauls; each has its own store
- **Store Operations / Store Contents**: add images, charts and files; save and load archives
- **Manifests**: create and manage sync manifests
- **Publishing**: expose published hauls on the `:5000` front door
- **Serve**: run a manual registry or fileserver on a port you choose
- **Registry Login**: authenticate to container registries
- **Settings**: global hauler flags and defaults

## Authentication

### UI Authentication

`HAULER_UI_PASSWORD` is required and enables single-user password protection.
If it is empty, Wagon logs a configuration error and exits:

```bash
docker run -d \
  -v wagon-data:/data \
  -p 127.0.0.1:8080:8080 \
  -e HAULER_UI_PASSWORD=mys3cr3t \
  wagon:local
```

- Sessions expire after 24 hours
- Cookie is httpOnly and SameSite=Strict
- No multi-user support - single shared password
- State-changing requests from another browser origin are rejected (403)
- `HAULER_UI_INSECURE_NO_AUTH=true` disables authentication entirely; it logs a
  loud warning and must never be used on a reachable host

### Registry Login

Registry credentials are stored in Docker config format:

```bash
# Via UI: Use the "Registry Login" page
# Via CLI:
hauler login registry.example.com --username user --password pass
```

Credentials are stored at `/data/.docker/config.json` inside the container. They are **base64-encoded, not encrypted**: anyone who can read the volume can read them.

## Data Management

Backup, restore and upgrade steps for the named volume `wagon-data` are in the
README: [Backup and restore](../README.md#backup-and-restore-docker-run-named-volume)
and [Upgrade](../README.md#upgrade). Always back up before an upgrade.

## Logs

View container logs:

```bash
# Follow logs
docker logs -f wagon

# Last 100 lines
docker logs --tail 100 wagon
```

Job logs are also available in the UI under "Job History".

## Troubleshooting

### Container Won't Start

1. **Port 8080 already in use**
   ```bash
   # Check what's using port 8080
   lsof -i :8080
   # Or use a different port
   docker run -p 9090:8080 ...
   ```

2. **Permission denied on /data**
   ```bash
   # Check the volume is writable (the container runs as root)
   docker run --rm --entrypoint ls -v wagon-data:/data wagon:local -ld /data
   ```

### Registry Login Fails

1. **Incorrect credentials** - Verify username and password
2. **Registry URL** - Include the full registry URL (e.g., `registry.example.com`, not `https://registry.example.com`)
3. **Existing auth conflict** - Remove old credentials:
   ```bash
   docker exec wagon rm /data/.docker/config.json
   docker restart wagon
   ```

### Store Operations Fail

1. **Out of disk space** - Check volume space:
   ```bash
   docker exec wagon df -h /data
   ```

2. **Temp directory full** - Clear temp files:
   ```bash
   docker exec wagon sh -c 'rm -rf /data/tmp/*'
   ```

3. **Network issues** - Check connectivity to registries from within container:
   ```bash
   docker exec wagon wget -O- https://registry.example.com/v2/
   ```

### Job Stuck Running

1. Check job logs in the UI
2. Cancel the job from the Job History page
3. If unresponsive, restart the container:
   ```bash
   docker restart wagon
   ```

### Serve Operations Not Accessible

To serve hauls to a cluster, follow [Serving hauls to a disconnected
cluster](./serving.md).

Published hauls and manual serves listen on their own ports, and each one must
be published from the container:

- **Publishing front door**: port 5000 (`HAULER_UI_REGISTRY_PORT`) needs `-p 127.0.0.1:5000:5000`
- **Serve page**: the port you chose there. The defaults are 5002 for the
  registry (`-p 127.0.0.1:5002:5002`) and 5001 for the fileserver
  (`-p 127.0.0.1:5001:5001`).

The Compose file publishes `PORT` (default 8080), `HAULER_UI_REGISTRY_PORT`
(default 5000) and the Serve defaults 5002 and 5001 on `HAULER_UI_BIND_ADDR`
(default `127.0.0.1`). A Serve port other than 5002 or 5001 needs its own line
under `ports:`.

If the Serve page shows a process as **crashed**, the message under it is
hauler's last error. For example, `bind: address already in use` means the port
is taken.

## Development

For development with live reload:

```bash
# Backend
cd backend
go run .

# Frontend (separate terminal)
cd web
npm run dev
```

The frontend dev server runs on port 5173.

### Quality Checks

```bash
make lint   # Run all linters
make test   # Run all tests
make build  # Verify build succeeds
```

## Additional Resources

- [Serving hauls to a disconnected cluster](./serving.md)
- [Persistence Documentation](./persistence.md)
- [Development Guide](./development.md)
- Rancher Government Hauler CLI documentation
