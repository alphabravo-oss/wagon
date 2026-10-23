<p align="center">
  <img src="web/public/wagon-logo.svg" alt="Wagon logo" width="120">
</p>

<h1 align="center">Wagon</h1>

<p align="center">A graphical control plane for Hauler, built by AlphaBravo.</p>

[![CI](https://github.com/alphabravo-oss/wagon/actions/workflows/ci.yml/badge.svg)](https://github.com/alphabravo-oss/wagon/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)

<p align="center">
  <img src="assets/ui-screenshot-1.png" alt="Wagon Screenshot" width="600">
</p>

A graphical, single-container control plane for [Rancher Government Hauler](https://github.com/rancherfederal/hauler) — manage isolated airgap content stores and serve them to disconnected clusters, no CLI required.

**NOTE**: This project is in ALPHA while we test it. We will release a stable version in the coming weeks.

## Overview

Wagon is a **single-container web application** that provides full operational parity with the Hauler CLI. It wraps the power of Hauler's command-line interface in an intuitive graphical experience, making airgap operations accessible to users of all skill levels.

### Key Features

- **Multi-Haul Workspaces** — First-class isolated hauls, each with its own content store; operate on them independently
- **Store Operations** — Add images, charts, and files to your store; sync registries; save/load archives; extract content
- **Registry Management** — Login/logout to Docker Hub, GHCR, and other container registries
- **Publishing** — Expose every published haul through a single host-routed registry front door (plus direct file serving at `/h/`), with TLS; registries start on demand and idle-reap
- **Serve Operations** — Start an embedded container registry or HTTP fileserver
- **Job Management** — Background task execution with streaming logs and job history
- **Manifest Management** — Create and edit Hauler manifests with Monaco editor
- **Authentication** — Optional password-based UI access control
- **Observability** — Prometheus metrics at `/metrics`, liveness `/healthz` + readiness `/readyz` probes, and structured JSON logging
- **Single Container** — Everything packaged in one Alpine-based image for simple deployment

## Quick Start

### Docker (Recommended)

```bash
# A UI password is required; Wagon refuses to start without one.
docker run -d \
  --name wagon \
  -p 127.0.0.1:8080:8080 \
  -e HAULER_UI_PASSWORD='choose-a-strong-password' \
  -v wagon-data:/data \
  ghcr.io/alphabravo-oss/wagon:latest

# Or use Docker Compose (set HAULER_UI_PASSWORD in deploy/.env first)
cp deploy/.env.example deploy/.env   # then edit HAULER_UI_PASSWORD
docker compose -f deploy/docker-compose.yml up -d
```

Access the UI at `http://localhost:8080` and log in with that password.

By default Wagon is only reachable from the machine it runs on. See
[Exposing Wagon to other machines](#exposing-wagon-to-other-machines).

### First Steps

1. **Open the UI** — Navigate to `http://localhost:8080` in your browser
2. **Configure Hauler** — Set global flags in Settings if needed
3. **Login to a Registry** — Use the Registry page to authenticate (e.g., Docker Hub)
4. **Add Content** — Go to Store → Add, and add an image, chart, or file
5. **Save Your Store** — Use Store → Save to create a portable archive

## Development

### Prerequisites

- **Go** 1.24+
- **Node.js** 20+
- **Make** (optional, for convenience commands)

### Project Structure

```
wagon/
├── backend/           # Go backend server
│   └── internal/      # Internal packages
│       ├── auth/      # Authentication & sessions
│       ├── config/    # Configuration management
│       ├── hauler/    # Hauler CLI integration
│       ├── jobrunner/ # Background job execution
│       ├── manifests/ # Manifest CRUD operations
│       ├── registry/  # Registry login/logout
│       ├── serve/     # Registry & fileserver serving
│       ├── settings/  # Global settings management
│       ├── sqlite/    # Database operations
│       └── store/     # Store operations
├── web/               # React frontend
│   └── src/
│       ├── components/ # Reusable UI components
│       ├── contexts/   # React Context providers
│       ├── pages/      # Page components
│       └── lib/        # Utilities and API client
└── deploy/            # Deployment configurations
```

### Running Locally

```bash
# Install dependencies
make deps

# Run backend (terminal 1)
cd backend && go run .

# Run frontend (terminal 2)
cd web && npm run dev

# Access at http://localhost:5173
```

### Build Commands

```bash
# Build everything
make build

# Build backend only
make build-backend

# Build frontend only
make build-frontend

# Run tests
make test

# Lint code
make lint
```

## Configuration

Wagon is configured via environment variables.

> `HAULER_UI_*` names remain supported as the stable configuration interface
> for compatibility with existing deployments; they now configure Wagon.

### Server & Auth

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP UI/API server port (now honored in code) |
| `HAULER_UI_PASSWORD` | (none, **required**) | Shared password for UI/API login. Wagon refuses to start if it is empty |
| `HAULER_UI_BIND_ADDR` | `127.0.0.1` | Compose only: host address the ports are published on. Set deliberately to expose Wagon |
| `HAULER_UI_TRUSTED_ORIGINS` | (empty) | Comma-separated extra origins allowed to make changes (reverse proxy that rewrites `Host`) |
| `HAULER_UI_INSECURE_NO_AUTH` | `false` | **Danger.** `true` allows starting with no password, i.e. no authentication at all. Local throwaway testing only |
| `HAULER_UI_LOGIN_RATE` | `5` | Max login attempts per minute per client IP (429 beyond) |
| `HAULER_UI_TRUST_PROXY` | `false` | When true, trust the rightmost `X-Forwarded-For` hop for login rate-limit IP keying (only behind a trusted reverse proxy) |

### Published Registry / Files (the multi-haul front door)

| Variable | Default | Description |
|----------|---------|-------------|
| `HAULER_UI_REGISTRY_PORT` | `5000` | Single host-routed port serving all published haul registries |
| `HAULER_UI_REGISTRY_DOMAIN` | (empty) | Base domain for `<slug>.<domain>` registry routing |
| `HAULER_UI_REGISTRY_IDLE` | `5m` | Idle timeout before a published registry subprocess is reaped (Go duration) |
| `HAULER_UI_REGISTRY_TLS_CERT` | (empty) | Path to a TLS cert for the registry port (self-signed if unset) |
| `HAULER_UI_REGISTRY_TLS_KEY` | (empty) | Path to the TLS key |
| `HAULER_UI_PUBLISH_USER` | (empty) | HTTP Basic auth username guarding the published registry **and** `/h/` file endpoints. If **both** user & password are empty the endpoints are OPEN (a startup WARNING is logged) |
| `HAULER_UI_PUBLISH_PASSWORD` | (empty) | Basic auth password for the above |

### Jobs & Logging (observability)

| Variable | Default | Description |
|----------|---------|-------------|
| `HAULER_UI_MAX_CONCURRENT_JOBS` | `2` | Max background jobs run at once |
| `HAULER_UI_LOG_FORMAT` | `json` | UI server log format: `json` or `text` |
| `HAULER_UI_LOG_LEVEL` | `info` | UI server log level: `debug` \| `info` \| `warn` \| `error` |

### Hauler CLI Passthrough & Paths

| Variable | Default | Description |
|----------|---------|-------------|
| `HAULER_BINARY` | `hauler` | Hauler binary name/path |
| `HAULER_LOG_LEVEL` | `info` | Hauler CLI log level |
| `HAULER_IGNORE_ERRORS` | `false` | Continue operations despite errors |
| `HAULER_RETRIES` | `0` | Retries for hauler ops (also settable in the Settings UI) |
| `HAULER_DIR` | `/data` | Hauler working directory |
| `HAULER_STORE_DIR` | `/data/store` | Store directory path |
| `HAULER_TEMP_DIR` | `/data/tmp` | Temporary files directory |
| `DOCKER_CONFIG` | `/data/.docker` | Docker auth config directory |
| `DATABASE_PATH` | `/data/app.db` | SQLite database path |

> **Source of truth**: See `deploy/.env.example` for the complete list of documented environment variables.

### Persistent Data

All persistent data is stored in `/data`:

- **Hauler store** — Container images, charts, and files
- **SQLite database** — Jobs, settings, and manifests (`app.db`)
- **Docker config** — Registry authentication (`.docker/config.json`)

Mount this directory as a volume to persist data across container restarts.

## Deployment

### Docker Compose

```yaml
services:
  wagon:
    image: ghcr.io/alphabravo-oss/wagon:latest
    ports:
      - "127.0.0.1:8080:8080"   # Main UI (this host only)
      - "127.0.0.1:5000:5000"   # Registry serve
      - "127.0.0.1:5001:5001"   # Fileserver serve
    volumes:
      - wagon-data:/data
    environment:
      - HAULER_UI_PASSWORD=your-password  # Required

volumes:
  wagon-data:
```

### Exposing Wagon to other machines

Wagon's defaults are local-only: ports are published on `127.0.0.1`, and a
UI password is required. To make it reachable from other machines, do it
deliberately:

1. Keep a strong `HAULER_UI_PASSWORD` set.
2. Publish on a specific interface: set `HAULER_UI_BIND_ADDR` in `deploy/.env`
   to that interface's IP (or `0.0.0.0` for all interfaces), or use
   `-p <ip>:8080:8080` with `docker run`.
3. Prefer a TLS-terminating reverse proxy in front of the UI. If the proxy
   rewrites the `Host` header, list the public origin in
   `HAULER_UI_TRUSTED_ORIGINS` (e.g. `https://wagon.example.com`).
4. Set `HAULER_UI_PUBLISH_USER` / `HAULER_UI_PUBLISH_PASSWORD` if the published
   registry port should not be open to that network.

State-changing API requests from a different browser origin are rejected
(403), and JSON endpoints require `Content-Type: application/json` (415
otherwise). Jobs are created through the typed endpoints (store, registry,
...); there is no generic command endpoint.

### Kubernetes

You can deploy Wagon to Kubernetes using the Docker image. Ensure to:

1. Create a PersistentVolumeClaim for `/data`
2. Set environment variables as needed
3. Expose the service on your desired port

Example deployment configuration is available in `deploy/kubernetes/`.

### Building Your Own Image

```bash
# Build multi-stage Docker image
make docker-build

# Tag for your registry
docker tag wagon:latest your-registry/wagon:latest

# Push to registry
docker push your-registry/wagon:latest
```

## Architecture

### Backend (Go)

- **Standard library HTTP server** — No external web framework
- **SQLite (WAL mode)** — Embedded database for persistence
- **SSE (Server-Sent Events)** — Real-time log streaming
- **Async job runner** — Background process execution
- **Lazy publish registries** — Published haul registries start on demand and are idle-reaped

### Frontend (React)

- **React 19.1** — Modern React with hooks
- **Vite 8** — Fast build tool and dev server
- **React Router 7** — Client-side routing
- **Monaco Editor** — VS Code editor for manifests
- **Custom CSS** — Lightweight styling without frameworks

## Contributing

We welcome contributions! Please follow these guidelines:

1. **Write tests** for new functionality
2. **Run `make lint`** before committing
3. **Follow existing code style** and patterns
4. **Update documentation** as needed

## Hauler License

Copyright © 2025 Rancher Government, Inc.

 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.

## Support on Hauler (not the UI)

- **Issues**: [GitHub Issues](https://github.com/rancherfederal/hauler/issues)
- **Documentation**: [Hauler Docs](https://rancherfederal.github.io/hauler/)
- **Community**: [Rancher Government Slack](https://ranchergovernment.slack.com/)

## Support on Wagon

- **Issues**: [GitHub Issues](https://github.com/alphabravo-oss/wagon/issues)
- **Documentation**: [Documentation](https://github.com/alphabravo-oss/wagon/tree/main/docs)

## Built by AlphaBravo

AlphaBravo is a SDVOSB Company providing DevSecOps / Cloud / AI solutions to Government and Commercial organizations. We are committed to delivering secure and innovative technology solutions tailored to your needs.

For more information visit us at https://alphabravo.io.
