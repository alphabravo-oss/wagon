# Serving hauls to a disconnected cluster

Use **Publishing** to serve hauls to clusters. It is the one recommended way:
Wagon runs every published haul behind two ports.

| Port | What it serves | Protocol |
|---|---|---|
| `8080` | The UI and API, plus files at `/h/<slug>/<name>` | HTTP |
| `5000` | Container images and charts (OCI) for every published haul | HTTPS |

Port 5000 routes by **Host header**. A pull for `<slug>.<registry-domain>:5000`
reaches the haul with that slug. Registries must live at the host root (`/v2/`),
so one port and a hostname per haul is how many hauls share one endpoint.
For the design, see [multi-haul-serving.md](./multi-haul-serving.md).

## 1. Start Wagon with a registry domain

Pick a base domain that clients can resolve, for example `registry.example.com`.
Each haul is then served at `<slug>.registry.example.com:5000`.

```bash
docker run -d --name wagon \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:5000:5000 \
  -v wagon-data:/data \
  -e HAULER_UI_PASSWORD='choose-a-strong-password' \
  -e HAULER_UI_REGISTRY_DOMAIN=registry.example.com \
  ghcr.io/alphabravo-oss/wagon:latest
```

With Docker Compose, set `HAULER_UI_REGISTRY_DOMAIN` in `deploy/.env`.

These binds only accept connections from the Wagon host itself. To serve other
machines, bind the ports to an interface they can reach. See
[Exposing Wagon to other machines](../README.md#exposing-wagon-to-other-machines).
A cluster that only pulls images needs port 5000, not 8080.

## 2. Publish a haul

1. In the UI, add images to a haul (**Store Operations** page, with the haul active).
2. Open the haul (**Hauls**, then the haul), go to its **Client Access** tab, and click **Publish**.
3. The **Publishing** page lists the haul with its registry host,
   for example `demo.registry.example.com:5000`, and its files URL `/h/demo/`.

## 3. Let clients resolve the haul's hostname

Choose one:

- **Wildcard DNS (best):** point `*.registry.example.com` at the Wagon host.
  Every haul you publish later resolves with no extra work.
- **One DNS record per haul:** `demo.registry.example.com` pointing at the Wagon host.
- **No DNS:** add a line to `/etc/hosts` on each client and node. `/etc/hosts`
  has no wildcards, so add one line per haul:

  ```
  10.0.0.5  demo.registry.example.com
  ```

## 4. Trust the registry certificate

Port 5000 always uses HTTPS. Choose one certificate:

- **Self-signed (the default).** Wagon makes a certificate for
  `*.<registry-domain>`, `<registry-domain>` and `localhost`. Copy it to each client:

  ```bash
  openssl s_client -connect demo.registry.example.com:5000 \
    -servername demo.registry.example.com </dev/null 2>/dev/null \
    | openssl x509 > wagon-registry.crt
  ```

  Wagon makes a **new** self-signed certificate every time it restarts, so clients
  must fetch it again after each restart. For anything long-lived, load your own
  certificate instead.

- **Your own certificate.** Use a certificate from your CA, ideally a wildcard
  `*.registry.example.com`. Either:
  - paste the PEM certificate and key on the **Publishing** page under
    **Registry TLS** (it takes effect at once and is kept in `/data`), or
  - mount the files and set `HAULER_UI_REGISTRY_TLS_CERT` and
    `HAULER_UI_REGISTRY_TLS_KEY` to their paths in the container.

  Clients then trust your CA certificate, not Wagon's.

Install the certificate (Wagon's self-signed one or your CA's) on each client:

- **Docker:** no restart needed.

  ```bash
  sudo mkdir -p /etc/docker/certs.d/demo.registry.example.com:5000
  sudo cp wagon-registry.crt /etc/docker/certs.d/demo.registry.example.com:5000/ca.crt
  ```

- **k3s or rke2:** reference it in `registries.yaml` (next step).

## 5. Pull

**Docker:**

```bash
docker pull demo.registry.example.com:5000/library/busybox:latest
```

Images are served without their upstream registry host, so
`docker.io/library/busybox` is `library/busybox`. To see the exact names a haul holds:

```bash
curl --cacert wagon-registry.crt https://demo.registry.example.com:5000/v2/_catalog
```

**k3s or rke2:** make the published haul a mirror for the registries your
workloads already name, so manifests do not change. Write
`/etc/rancher/k3s/registries.yaml` (rke2: `/etc/rancher/rke2/registries.yaml`)
on every node, then restart k3s or rke2:

```yaml
mirrors:
  docker.io:
    endpoint:
      - "https://demo.registry.example.com:5000"
  ghcr.io:
    endpoint:
      - "https://demo.registry.example.com:5000"
configs:
  "demo.registry.example.com:5000":
    tls:
      ca_file: /etc/rancher/k3s/wagon-registry.crt
```

Add a `rewrite` only when the haul stores an image under a different path than
the one pods ask for. For example, if a haul was built with
`hauler store add image --rewrite` to put images under `mirror/`:

```yaml
mirrors:
  docker.io:
    endpoint:
      - "https://demo.registry.example.com:5000"
    rewrite:
      "^library/(.*)": "mirror/library/$1"
```

## 6. Optional: require a password for pulls

By default the published registry and files are open to anyone who can reach
them (Wagon logs a warning at startup). To require HTTP Basic auth on port 5000
and on `/h/`, set both variables:

```bash
-e HAULER_UI_PUBLISH_USER=puller \
-e HAULER_UI_PUBLISH_PASSWORD='another-strong-password'
```

Then clients log in:

- Docker: `docker login demo.registry.example.com:5000`
- k3s or rke2: add an `auth:` block under the host in `configs`:

  ```yaml
  configs:
    "demo.registry.example.com:5000":
      auth:
        username: puller
        password: another-strong-password
      tls:
        ca_file: /etc/rancher/k3s/wagon-registry.crt
  ```

## 7. Files

Files and charts in a published haul are served on the UI port, by path:

```bash
curl http://wagon-host:8080/h/demo/                  # JSON list of files
curl -fLO http://wagon-host:8080/h/demo/install.sh   # download one file
```

Add `-u puller:another-strong-password` when publish auth is on. Only published
hauls are reachable at `/h/`.

## When to use the Serve page instead

The **Serve** page is an advanced tool. It runs one hauler registry
(`hauler store serve registry`) or fileserver by hand for the active haul. Use it to:

- test a haul with hauler's own server before you publish it,
- get a writable registry (clear **Readonly**) to push into,
- browse a haul through hauler's fileserver.

It is not for clusters:

- Its defaults are port `5002` for the registry and `5001` for the fileserver, so
  they don't collide with the UI (8080) or Publishing (5000).
- You must publish whatever port you choose from the container, for example
  `-p 127.0.0.1:5002:5002`.
- There is no Host routing, no Basic auth and no shared certificate.
- Its processes stop when Wagon restarts.

If a serve exits (for example, because its port is already in use), the page shows it as
**crashed** with hauler's error message.
A serve is meant to run until you stop it, so any exit you did not ask for counts as
**crashed**, even a clean exit with status 0 (shown as `exited unexpectedly (exit status 0)`).
If Wagon can't record a stop in its database, the stop request fails with an error and the
serve keeps running; try again once the database is writable.
