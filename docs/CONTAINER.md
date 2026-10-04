# Container Image

## Generic usage and behavior

Release images are published to `ghcr.io/cnf/arbiter` for `latest` and each
release tag. The image is multi-architecture; Docker selects the matching
platform.

The default command starts the server and requires a config file at
`/config/arbiter.yaml`. The image declares `/config` and `/data` as volumes.
Mount a config at the expected path and persist `/data` if you want its contents
to survive container replacement.

Before checking for the config file, the entrypoint copies the bundled config
examples into `${DATADIR}/examples` if that directory does not already exist.
With the default `DATADIR=/data`, the full example is at
`/data/examples/arbiter.example.yaml`. This is a one-time copy: if
`${DATADIR}/examples` already exists, the image does not add or overwrite
examples when starting a newer version. The default server command still exits
if its config file is missing; copying examples does not create the active
config for you.

The container runs as UID/GID `1234:1234`. Mounted config must be readable and
`DATADIR` must be writable by that user. Set `storage.path` in your config to a
path under `${DATADIR}` (for example, `/data/arbiter.db`) if the event store
should persist in the data mount. Storage is disabled when that config setting
is omitted.

The entrypoint also accepts `catalog-convert` (uses the same config file),
`sh`, and `-h` commands. Its runtime environment variables are:

| Variable      | Default                     | Purpose                                                          |
| ------------- | --------------------------- | ---------------------------------------------------------------- |
| `PORT`        | `8080`                      | Server port inside the container. Publish this port from Docker. |
| `BINDHOST`    | `0.0.0.0`                   | Address the server binds inside the container.                   |
| `CONFIGDIR`   | `/config`                   | Directory containing `arbiter.yaml`.                             |
| `CONFIG_FILE` | `${CONFIGDIR}/arbiter.yaml` | Full config path; overrides the default file under `CONFIGDIR`.  |
| `DATADIR`     | `/data`                     | Directory for persisted data and bundled examples.               |

Arbiter expands `${NAME}` references in its config from the container's
environment. Pass any referenced values into the container explicitly; the
image does not enable provider credentials by default. Provider endpoints must
also be reachable from inside the container. In particular, `localhost` means
the container itself; use `host.docker.internal` to reach a provider running on
the host.

## Quick start with Docker Compose

This walkthrough assumes the working directory contains only the repository's
`compose.yaml`. Docker and the Docker Compose plugin must be installed.

The bind-mounted data directory must be writable by the container's UID/GID
`1234:1234`. Create it before initialization:

```bash
mkdir -p arbiter-data
```

If this directory is not writable by UID `1234` on Linux, adjust its ownership,
for example with `sudo chown 1234:1234 arbiter-data`.

Pull the image, then run a one-off shell command. The entrypoint copies its
bundled examples into the mounted data directory before launching the shell, so
this initialization does not require an Arbiter config:

```bash
docker compose pull
docker compose run --rm arbiter sh -c 'exit'
```

Copy the full example config from the host data directory:

```bash
cp arbiter-data/examples/arbiter.example.yaml arbiter.yaml
```

Edit `arbiter.yaml` for your providers and routing policies. Uncomment and set
`storage.path` to `/data/arbiter.db` if you want the event store in the mounted
data directory. Provider endpoints must be reachable from the container; for a
provider running on the host, use `host.docker.internal` rather than
`localhost`.

The Compose file leaves the config bind mount commented for this first-run
sequence. Once `arbiter.yaml` exists, uncomment that mount under the service's
`volumes` in `compose.yaml`. If your config references environment values such
as `${ANTHROPIC_API_KEY}`, uncomment only the corresponding provider entries
under `environment` and supply those values to Compose from the shell or a
Compose `.env` file. The image settings are shown as commented defaults; if you
change `PORT`, update the port mapping too. To use a release other than
`latest`, change the image tag in `compose.yaml`.

Start the service and check its health:

```bash
docker compose up -d
curl http://localhost:8080/health
docker compose ps
```

The Compose healthcheck also checks `GET /health`. The API and admin UI are
available on port 8080. See [docs/clients.md](docs/clients.md) for API details
and [docs/observability.md](docs/observability.md) for the admin surface. Follow
logs with `docker compose logs -f arbiter`; stop the service with
`docker compose down`. The config and data remain in their host directories.
