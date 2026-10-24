# Known Limitations

This document describes known limitations of Wagon and how the application surfaces these limitations to users.

## Platform-Specific Limitations

### Podman Tarballs Not Supported

**Issue**: Hauler cannot load tarballs created by Podman.

**Affected Operation**: Load Archive

**UI Indication**:
- Warning banner on the Load Archive page: "Docker-saved tarball supported as of v1.3; Podman tarballs not supported"

**Workaround**: Use Docker to create tarballs, or use the Hauler CLI directly with supported formats.

### Copy Operations Require Root Registry Login

**Issue**: When copying to a registry path (e.g., `registry.example.com/myproject`), you must login to the registry root without a path first.

**Affected Operation**: Copy Store

**UI Indication**:
- Warning on Copy Store page: "Must login to registry root (no path) before copy to path"
- Inline help text explains the login requirement

**Workaround**: Login to `registry.example.com` (without `/myproject`) before attempting the copy operation.

## Feature Limitations

### Remove is Experimental

**Issue**: The `hauler store remove` command is experimental and may have unexpected behavior.

**Affected Operation**: Remove Artifacts

**UI Indication**:
- Warning banner: "Feature is experimental"
- Requires explicit confirmation step
- Force option available to bypass confirmation

**Recommendation**: Test carefully in non-production environments first.

### Serve Port Conflicts

**Issue**: A serve fails to start when its port is already taken. 8080 is the
web UI and 5000 is the Publishing front door, so neither can be used for a
serve. The Serve page defaults to 5002 for the registry and 5001 for the
fileserver.

**Affected Operation**: Serve page (Registry, Fileserver)

**UI Indication**:
- Port fields show the defaults (5002 registry, 5001 fileserver)
- The start request fails with the reason hauler gave (for example
  `bind: address already in use`), and the run is listed as crashed with that
  reason

**Workaround**: Pick a free port and publish it from the container:
```bash
docker run -p 127.0.0.1:8080:8080 -p 127.0.0.1:5002:5002 -p 127.0.0.1:5001:5001 -v ./data:/data wagon:latest
```

## Storage Limitations

### Temporary Directory Space Requirements

**Issue**: Sync, save, and load operations require significant temporary disk space.

**Affected Operations**: Sync from Manifests, Save Archive, Load Archive

**UI Indication**:
- Settings page shows configured temp directory path
- Job failures due to disk space show clear error messages
- Store Operations page displays temp directory location

**Workaround**: Ensure adequate disk space or configure a different temp directory via `HAULER_TEMP_DIR`.

### Store Size Grows Unbounded

**Issue**: The store directory grows as content is added; there is no automatic cleanup.

**Affected Operations**: All store operations

**UI Indication**:
- No built-in size indicator (planned for future release)
- Settings page shows store directory path

**Workaround**: Monitor store size on host and use Remove Artifacts operation as needed:
```bash
du -sh ./data/store
```

## Authentication Limitations

### Single-User Only

**Issue**: The UI password protection is designed for single-user scenarios only.

**Affected Feature**: HAULER_UI_PASSWORD

**UI Indication**:
- Login page shows single password field (no username)
- Documentation states "single shared password only"

**Limitations**:
- No user accounts or roles
- No audit logging of which user performed actions
- Shared password for all authenticated users

**Recommendation**: For multi-user scenarios, use external authentication (reverse proxy) or run separate instances.

### Session Expiration

**Issue**: Sessions expire after 24 hours with no configurable duration.

**Affected Feature**: HAULER_UI_PASSWORD

**UI Indication**:
- No UI indication of session expiration
- Users are redirected to login when session expires

**Workaround**: Refresh the page and re-enter password when prompted.

## Operational Limitations

### Job Concurrency

**Issue**: Hauler CLI operations run sequentially; there is no job queue management.

**Affected Operations**: All long-running operations

**UI Indication**:
- Multiple jobs can be started simultaneously
- Each job runs independently (no coordination)
- Jobs may compete for resources

**Recommendation**: For large operations, run jobs sequentially rather than in parallel.

### No Job Scheduling

**Issue**: Jobs cannot be scheduled for future execution.

**Affected Operations**: All job-based operations

**Workaround**: Use external tools (cron, Kubernetes jobs) to trigger API endpoints at scheduled times.

### Streaming Latency

**Issue**: Job log streaming via SSE may have latency in high-load situations.

**Affected Operations**: All job viewing

**UI Indication**:
- Logs may appear delayed during heavy processing
- Refresh button available to manually update

## Network Limitations

### No Proxy Configuration UI

**Issue**: HTTP/HTTPS proxy configuration must be set via environment variables.

**Affected Operations**: All network operations (sync, add, copy)

**Environment Variables**:
```bash
HTTP_PROXY=http://proxy.example.com:8080
HTTPS_PROXY=http://proxy.example.com:8080
NO_PROXY=localhost,127.0.0.1
```

**UI Indication**:
- No proxy settings in UI
- Connection failures may indicate proxy-related issues

### Insecure Registry Warnings

**Issue**: Connecting to insecure registries (HTTP, self-signed certs) requires explicit flags.

**Affected Operations**: Add Image, Add Chart, Add File, Sync from Manifests, Copy Store

**UI Indication**:
- Toggle options available: "Insecure Skip TLS Verify", "Plain HTTP"
- Warning icons next to these options

**Recommendation**: Use secure registries when possible. Only use insecure options in isolated networks.

## Version-Dependent Limitations

### Feature Detection Based on Installed Hauler

**Issue**: Available features depend on the Hauler CLI version installed in the container.

**UI Indication**:
- Settings page shows detected Hauler version
- Some options only appear when supported by the installed version
- "Refresh capabilities" button to re-detect features

**Example**: `--add-dependencies` flag for charts only appears if Hauler v1.0.0+ is detected.

## Hauler CLI Limitations (Inherited)

The following limitations are inherited from the Hauler CLI itself:

- OCI artifacts require specific structure
- Some image registries have rate limits
- Large manifests may take significant time to process
- Certain Helm chart features may not be supported

For the latest Hauler CLI documentation and known issues, refer to the official Rancher Government documentation.
