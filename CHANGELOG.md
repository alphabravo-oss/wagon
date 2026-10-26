# Changelog

All notable changes to Wagon will be documented in this file.

## [Unreleased]

### Security

- Serve no longer passes caller-chosen paths straight to hauler, which runs as
  root. `directory` on `POST /api/serve/registry` and `/api/serve/fileserver`
  must be a folder inside the haul's own folder and outside its store; before,
  `{"directory":"/etc"}` served `/etc` and let file artifacts overwrite files
  there. `tlsCert` and `tlsKey` must be files in `<data>/certs`. Relative paths
  are taken from those folders, symlinks are resolved before the check, and
  anything else gets HTTP 400 before hauler starts. Serve request bodies are
  capped at 64 KiB (HTTP 413).
- Store endpoints reject values that hauler takes as positional arguments
  (`imageRef`, chart `name`, `filePath`, `url`, `match`, `artifactRef`) when
  they start with `-`, with HTTP 400.
- Add File, Extract and Copy no longer let a signed-in user make hauler,
  which runs as root, read or write any host path. `filePath` on
  `POST /api/store/add-file` must be a file in `<data>/imports`; before,
  `/etc/shadow` or `app.db` could be added to the store and served on the
  unauthenticated serve port. `url` must be an `http://` or `https://` URL
  with a host, and Wagon now downloads it itself and gives hauler only the
  downloaded file: hauler's file getter opened `filepath.Join(host, path)`
  on disk whatever the scheme, so `http://x/../../etc/host_secret.txt`,
  `http://app.db` and `http://.docker` read host files (`url` also took bare
  paths and `file://`). `outputDir` on
  `POST /api/store/extract` and a `dir://` target on `POST /api/store/copy`
  must be folders inside the haul's own folder, outside its `store`, `archives`
  and `registry` folders; before, `{"outputDir":"/etc/cron.d"}` wrote artifact
  content there as root. Extract without `outputDir` now writes to
  `<haul>/extracted` instead of the data folder itself. The add-file `name`
  must not contain `/` or `\` and must not be `.` or `..`. All of these get
  HTTP 400 before hauler starts. Local files for Add File now go in
  `<data>/imports` first (see `docs/persistence.md`).
- Add File `url` downloads can no longer reach Wagon's host or internal
  network (SSRF). Every connection, including each redirect hop, is checked
  against its resolved address. Loopback, link-local (including the
  `169.254.169.254` cloud metadata address), unspecified, multicast and
  reserved addresses are always refused. Private ranges (RFC 1918, CGNAT
  `100.64.0.0/10`, IPv6 `fc00::/7`) are refused unless listed in the new
  `HAULER_UI_FETCH_ALLOW_CIDRS`. Downloads are capped at
  `HAULER_UI_FETCH_MAX_BYTES` (default 2 GiB) and `HAULER_UI_FETCH_TIMEOUT`
  (default `30m`), and only `http` and `https` are followed. Before, a
  signed-in user could make Wagon fetch the metadata service or other
  internal hosts and serve the response from the store.
- Add Image, Add Chart and Sync check the hosts hauler will connect to against
  the same address policy (SSRF): the registry in `imageRef`, the host of
  `repoUrl`, the host of a chart `name` that is a URL (`oci://…`,
  `https://…/x.tgz`), and the Sync `registry`. A host that
  is blocked, or that does not resolve, gets HTTP 400 before a job is created.
  The 400 names the host but not the addresses it resolves to, and the lookup
  gives up after 10 seconds. Before, `{"imageRef":"169.254.169.254/latest/meta-data"}`,
  a `repoUrl` on `127.0.0.1` or `{"name":"oci://127.0.0.1:5000/charts/x"}`
  made hauler, which runs as root, connect there. Images and charts listed
  inside a Sync manifest are not checked yet.
  Only the first host is checked, not the hosts reached after it: redirects,
  registry auth realms, absolute chart URLs in a repository's `index.yaml`,
  chart dependencies, and images inside a chart. hauler also resolves the
  name again when the job runs, so a DNS answer that changes in between is
  not stopped.
- Sync and Load no longer let a signed-in user put root-only host files into
  a store, where the unauthenticated serve port then served them (card 69).
  A Sync `Files` manifest naming `/etc/host_canary.txt` (mode `0600`) added it
  to the store; a manifest planted through Import in the haul's `archives`
  folder made this reliable. Wagon now parses every Sync manifest and refuses
  any local path in it outside `<data>/imports`, the way hauler resolves it
  (relative to the data folder, URL-decoded, symlinks followed): `Files`
  paths, `ImageTxts` refs, charts helm would load from disk, and image cosign
  keys. Sync `filenames` must be files in `<data>/imports`. Load used any
  absolute archive path as-is and now takes a plain archive name from the
  haul's `archives` folder or a file in `<data>/imports`. All of these get
  HTTP 400 before hauler starts, and a refused Load no longer clears the
  store first. hauler now reads a checked copy of the manifest with a unique
  name, deleted when the job ends; before, the pasted-manifest file always had
  the same name and was deleted before the queued job could read it.
  Sync also refuses `products` and `productRegistry` (HTTP 400, any key
  casing, any value): for each product hauler pulled
  `<productRegistry>/hauler/<name>-manifest.yaml` from a registry the caller
  picked and ran it unchecked, which brought the host file read back. The
  checked copy keeps unquoted numbers as written (`version: 1.10` used to
  become `1.1`), and copies left by a stop mid-job are deleted at the next
  start, except one a queued job still needs.
- A Sync manifest's cosign key (`spec.images[].key` or the `hauler.dev/key`
  annotation) must be a regular file in `<data>/imports` or `<data>/certs`
  (card 111). `<data>/certs` is also the folder for the request's `key` once
  card 68 lands, so one key file can serve both. A folder or a missing file gets HTTP 400 before a job
  exists; before, a folder in imports passed the check. `Files` paths and every
  other local path in a manifest still have to be in `<data>/imports`.
- A Sync manifest's `http://` or `https://` `Files` path or `ImageTxts` ref
  gets HTTP 400 when `filepath.Join(host, path)` could name something in or
  above the data folder by the time the queued job runs: a first element that
  starts with `.` (`.docker`, `..`), that exists in `<data>`, or that Wagon
  creates there (`certs`, `hauls`, `imports`, `registry-tls`, `store`, `tmp`,
  the database and the configured store, temp and login paths). hauler reads
  that local form whenever it exists, so `https://certs/registry.key` passed
  the check while Serve's TLS key did not exist yet and read it once it did
  (card 111, review 101). A Charts or ThickCharts `repoURL` must be an
  `http`, `https` or `oci://` URL or a relative path; `file:` URLs in any case
  and absolute paths get HTTP 400.
- Registry login and logout reject a `registry` that starts with `-` (HTTP 400).
  JSON store request bodies are capped at 1 MiB (HTTP 413). Serve and store 400
  messages no longer include absolute data-folder paths.
- Serve no longer accepts `configFile` on `POST /api/serve/registry`. hauler's
  `--config` overrides every other flag, so a caller's config file could make a
  read-only registry writable and point its storage at any path in the
  container. A request that still sends `configFile` (any value) now gets HTTP
  400 instead of starting hauler, and the Serve page no longer has a Config File
  field. Wagon-generated config for Publishing is unchanged.

### Changed — secure defaults (breaking)

- Add File takes local files only from `<data>/imports`, and its `url` only as
  `http://` or `https://`. Wagon downloads the url itself, directly: it
  ignores `HTTP_PROXY`/`HTTPS_PROXY`, refuses private addresses unless
  `HAULER_UI_FETCH_ALLOW_CIDRS` lists them, and needs a `name` when the URL
  path has no file name (HTTP 400). Add Image, Add Chart and Sync refuse
  registries and chart repositories on private addresses unless
  `HAULER_UI_FETCH_ALLOW_CIDRS` lists them, and hosts Wagon cannot resolve
  (HTTP 400). Extract and Copy write only inside the
  haul's folder, and Extract without `outputDir` writes to `<haul>/extracted`.
  Requests that used other paths now get HTTP 400 (see Security above).
- **Breaking:** Sync reads manifest files only from `<data>/imports` (no
  manifest URLs in `filenames`), and every local path inside a manifest must
  be there too. A relative filename,
  including the default `hauler-manifest.yaml`, is now taken from
  `<data>/imports` instead of the data folder. Manifests may only contain
  `Files`, `Images`, `Charts`, `ThickCharts` and `ImageTxts` documents, and
  image keys must be files in `<data>/imports` or `<data>/certs` (no `~`
  paths or KMS URIs).
  Load takes a plain archive name from the haul's `archives` folder, as the
  haul page's Load button sends, and any other path only from
  `<data>/imports`; a relative path such as `sub/a.tar.zst` is now taken
  from `<data>/imports`. Move manifests, the files they name and archives to
  load into `<data>/imports` (see `docs/persistence.md`).
- **Breaking:** Sync no longer takes `products` or `productRegistry`, and the
  Sync page drops both fields. Put a product's manifest in `<data>/imports`
  and sync that file instead.
- `HAULER_UI_PASSWORD` is now required. Wagon refuses to start without it. The
  explicit, off-by-default `HAULER_UI_INSECURE_NO_AUTH=true` restores the old
  no-auth behaviour for throwaway local use and logs a warning.
- `deploy/docker-compose.yml` publishes ports on `127.0.0.1` by default; set
  `HAULER_UI_BIND_ADDR` to expose Wagon deliberately.
- State-changing requests from another browser origin are rejected (403); JSON
  endpoints require `Content-Type: application/json` (415); the archive upload
  requires an `X-Requested-With` header. `HAULER_UI_TRUSTED_ORIGINS` allows
  extra origins behind a Host-rewriting proxy.
- Removed the generic `POST /api/jobs` endpoint; jobs are created by the typed
  store/registry endpoints.
- Job API responses and the job event stream no longer send wildcard CORS
  headers and mask credential values (env overrides, `--password`).

### Changed

- Renamed the product from Hauler UI to **Wagon**.
- Moved repository references to `github.com/alphabravo-oss/wagon` and release
  images to `ghcr.io/alphabravo-oss/wagon`.
- Replaced the application logo, wordmark, and favicon with the Wagon branding.
- Renamed the haul page's **Publish** tab to **Client Access** so it no longer
  shares a name with the **Publishing** page.

### Removed

- Add Chart no longer has username/password fields, and
  `POST /api/store/add-chart` ignores `username` and `password`. Hauler ignores
  `--username`/`--password` on `store add chart`, and passing them exposed the
  credentials in the process argv.

### Fixed

- Registry login now sends the username and password to hauler. It runs
  `hauler login <registry> -u <user> --password-stdin`, and the password goes
  only to stdin: it is not in the process arguments, the environment, the job
  row or the job log. Before, Wagon set `HAULER_REGISTRY_USERNAME`/`PASSWORD`
  env vars that hauler 1.3.2 does not read, and stored the password in the job
  row. hauler saves credentials without checking them, so the UI and API now
  say the login was saved, not verified; a wrong password fails at pull or push.

## [0.1.0] - 2026-07-11

First tagged release. Finalizes the multi-haul + publish work and adds a round
of security, reliability, observability, and test hardening. Published as a
public multi-arch image (linux/amd64 + linux/arm64) at
`ghcr.io/alphabravo-oss/hauler-ui:0.1.0`.

### Added — Multi-haul serving (Publish layer)

Expose many hauls through hauler-ui's single front door instead of one port per
haul.

- **Publish a haul** (`POST /api/publish/{id}`): starts a read-only internal
  registry for that haul and registers a virtual host. A single host-routed
  reverse proxy (one port, default `:5000`) routes registry traffic to the right
  haul by `Host` header, so many hauls share one registry endpoint. Published
  state survives restarts.
- **Direct file serving** at `GET /h/<slug>/` (listing) and `/h/<slug>/<name>`
  (download), straight from each haul's store — no subprocess, public so air-gap
  consumers can `curl` them.
- **Registry TLS**: the registry endpoint is HTTPS. Load a CA-signed cert via
  `HAULER_UI_REGISTRY_TLS_CERT/KEY` or the Publishing page
  (`POST /api/publish/tls`); it takes effect with no restart. A self-signed
  wildcard is used until then.
- **UI**: a Publishing page (routes table + TLS management + client snippets)
  and Publish controls on each haul's Serve tab.
- Config: `HAULER_UI_REGISTRY_PORT` (default 5000),
  `HAULER_UI_REGISTRY_DOMAIN` (subdomain routing), `HAULER_UI_REGISTRY_TLS_CERT`
  / `_KEY`.

> Note: each published haul currently runs one `hauler store serve registry`
> subprocess; files are served in-process. See `docs/multi-haul-serving.md`.

### Changed — Multi-haul workspaces

Reworked the core model so a **haul** is now a first-class, isolated workspace
instead of a loose `.tar.zst` file. Multiple hauls can be built, edited, served,
and archived side by side without clearing or merging a shared store.

- **Isolated per-haul stores** — each haul owns its own OCI store directory
  (`/data/hauls/<slug>/store`); operations never affect other hauls.
- **Haul resource API** — `GET/POST /api/hauls`, `GET/PATCH/DELETE /api/hauls/{id}`,
  and `GET /api/hauls/{id}/archives[/{file}]` for create/rename/delete plus
  per-haul archive listing, download, and delete.
- **Haul-scoped store operations** — add/sync/save/load/copy/remove/extract/info
  and serve all accept a `haulId` (body) or `?haul=` (query) and target that
  haul's store; they fall back to a seeded **Default** haul.
- **Exact provenance** — store contents are tracked per haul, replacing the
  digest/name-matching heuristic.
- **Per-haul serving** — registry/fileserver servers bind to a haul's store, get
  isolated backend directories, and are guarded against port collisions.
- **New UI** — a Hauls list and a tabbed haul workspace (Overview, Contents,
  Add Content, Archives, Serve) with an active-haul switcher in the top bar.
- **Per-haul manifest library** — saved manifests are owned by a haul; names are
  unique per haul and the Manifests page is scoped to the active haul.
- **Live haul stats** — the Hauls list and switcher poll so item and archive
  counts stay current after operations complete.
- **Clearer paths** — the Store page and Settings now show the active haul's
  isolated store directory and the per-haul hauls root instead of a single
  global store path.

### Added — Observability

- **Prometheus metrics** at `/metrics` (request counts/durations, queued/running
  job gauges, published-haul gauge) — dependency-free text exposition.
- **Readiness probe** `/readyz` (503 unless the database is reachable and the
  `hauler` binary resolves), alongside the existing `/healthz` liveness probe.
- **Structured JSON logging** via `log/slog` (also routes existing stdlib logs);
  a per-request access log. Config: `HAULER_UI_LOG_FORMAT` (json/text),
  `HAULER_UI_LOG_LEVEL`.

### Added — Testing, CI & release

- Unit tests across the backend (`hauls`, `manifests`, `publish`, `config`,
  `registry`, `serve`, `settings`, `obs`, `auth`) and a **migration-upgrade
  regression test** that fails CI if a future migration drops populated stable
  tables. Minimal frontend test harness (vitest + jsdom).
- **GitHub Actions CI** (build/vet/test + frontend build/lint) and **Dependabot**
  (alerts, security fixes, weekly gomod/npm/actions updates).
- **Public multi-arch image** published to `ghcr.io/alphabravo-oss/hauler-ui`
  (amd64 + arm64) via buildx, on `main` pushes and `v*` tags.

### Changed — Registry serving is now lazy

- Published registries **start on the first pull and idle-reap** after
  `HAULER_UI_REGISTRY_IDLE` (default 5m), so the number of live
  `hauler serve registry` subprocesses tracks the active pull set rather than the
  total number of published hauls.

### Changed — Reliability & configuration

- SQLite now uses **WAL journaling + busy_timeout** to reduce lock contention.
- The UI/API server honors **`PORT`** (was hardcoded to `8080`).
- Job working directory is injectable (no longer hardcoded `/data`).
- Frontend on **Vite 8**; large files split into modules (`App.jsx`,
  `store/handler.go`).
- Repository moved to **`github.com/alphabravo-oss/hauler-ui`**; image, docs, and
  deploy references updated accordingly.

### Fixed

- **Job-log capture race**: `cmd.Wait()` ran concurrently with the stdout/stderr
  readers, truncating captured output for fast commands
  (`read: file already closed`). Pipes are now drained to EOF before reaping.
- Flaky `jobrunner` tests (single-writer test DB; nil-safe job polling).

### Security

- **Constant-time password comparison** (replaced a timing-leaky string compare).
- **`Secure` session cookie**, gated on TLS / `X-Forwarded-Proto`.
- **Optional HTTP Basic auth** on the published registry and `/h/` file endpoints
  (`HAULER_UI_PUBLISH_USER` / `HAULER_UI_PUBLISH_PASSWORD`; endpoints stay open
  when unset, with a startup warning).
- **Login rate limiting** (`HAULER_UI_LOGIN_RATE`; `HAULER_UI_TRUST_PROXY` for
  proxy-aware client-IP keying).
- Cleared all outstanding Dependabot alerts (js-yaml, dompurify, brace-expansion).
- **Additive-only migration policy** documented and enforced by a CI upgrade test.

## [0.1.0-alpha] - 2025-01-28

### Added

#### Store Operations
- Add container images to the store with platform specification
- Add Helm charts from repositories with dependency and image support
- Add local files or remote URLs to the store
- Sync store from manifest files with platform and registry overrides
- Save store as portable `.tar.zst` archives with checksums
- Load archives into the store with optional clear-before-load
- Copy store contents to external registries or directories
- Remove artifacts from the store using pattern matching
- View store contents with filtering by images, charts, and files
- Track source haul for each stored item

#### Registry Management
- Login to Docker Hub, GHCR, and other container registries
- Logout from registries with credential cleanup
- Secure credential storage using standard Docker auth pattern
- View stored credential information and paths

#### Serve Operations
- Start/stop embedded container registry (default port 5000)
- Start/stop embedded HTTP fileserver (default port 5001)
- TLS support with auto-generated self-signed certificates
- Custom TLS certificate and key paths
- Read-only mode for registry serving
- Configurable timeouts for fileserver
- Process monitoring with PID tracking and status display
- Access URLs for both registry and fileserver

#### Manifest Management
- Create, edit, and delete Hauler manifests
- Monaco Editor (VS Code editor) for YAML editing
- Manifest tagging for organization
- Download manifests as YAML files
- Store contents linked to manifests
- Support for product and product-registry manifests

#### Job Management
- Background job execution for all long-running operations
- Real-time log streaming via Server-Sent Events (SSE)
- Job history with timestamps and duration tracking
- Job status monitoring (queued, running, succeeded, failed)
- Exit code tracking and error reporting
- Bulk job deletion with confirmation
- Job detail pages with full command and output

#### Authentication & Security
- Optional password-based UI access control (`HAULER_UI_PASSWORD`)
- Session management with 24-hour expiration
- httpOnly and SameSite=Strict cookies
- Protected routes requiring authentication
- Password never stored in database (credential passthrough only)
- Path traversal protection in file operations

#### Settings & Configuration
- Global settings stored in SQLite database
- Environment variable precedence over database settings
- Configurable log level (debug, info, warn, error)
- Configurable retry count for failed operations
- Ignore errors flag for continued operations
- Default platform specification for multi-arch operations
- Default key path for signature verification
- Temporary directory path configuration

#### User Interface
- Dashboard with system health and Hauler CLI version
- Sidebar navigation organized by Main, Operations, and System
- Responsive design with mobile hamburger menu
- Job indicator showing running job count
- Form validation with error messages
- Loading states and progress indicators
- Success/error notification cards
- Confirmation dialogs for destructive actions
- Dark-themed UI with accent colors

#### API & Backend
- RESTful API with JSON responses
- SQLite database for persistence
- CRUD endpoints for jobs, settings, manifests, and store contents
- Health check endpoint (`/healthz`)
- Configuration endpoint showing all system paths
- Capabilities endpoint reporting Hauler CLI version
- Streaming log endpoints for real-time job output
- Archive download with range request support

#### Deployment
- Single-container Alpine-based Docker image
- Multi-stage build for minimal image size
- Docker Compose configuration with all ports exposed
- Environment variable configuration with defaults
- Persistent data volume at `/data`
- Comprehensive `.env.example` for configuration reference
- Makefile with build, lint, and test commands

### Documentation
- Comprehensive README with quick start guide
- Development guide with local setup instructions
- Runbook with common workflows and troubleshooting
- Persistence documentation explaining data storage
- Inline API documentation comments in Go backend

### Known Limitations
- Podman-generated tarballs are not supported for `store load`
- When copying to a registry path, login to the registry root first
- Large operations (sync, save) may require significant temporary space
- Self-signed certificates trigger browser security warnings

---

### Version Format

Changelog entries follow [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) format:
- **Added** - New features
- **Changed** - Changes to existing functionality
- **Deprecated** - Features to be removed in future releases
- **Removed** - Features removed in this release
- **Fixed** - Bug fixes
- **Security** - Security vulnerability fixes
