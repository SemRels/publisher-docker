# publisher-docker

[![Latest Release](https://img.shields.io/github/v/release/SemRels/publisher-docker?label=version&color=blue)](https://github.com/SemRels/publisher-docker/releases/latest)
[![CI](https://github.com/SemRels/publisher-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/SemRels/publisher-docker/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/SemRels/publisher-docker)](LICENSE)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/SemRels/publisher-docker/badge)](https://scorecard.dev/viewer/?uri=github.com/SemRels/publisher-docker)
[![REUSE status](https://api.reuse.software/badge/github.com/SemRels/publisher-docker)](https://api.reuse.software/info/github.com/SemRels/publisher-docker)

`@semrel/publisher-docker` publishes one image that already exists in a
Docker-compatible local daemon. It does not build the image. Semrel runs the
standalone `semrel-plugin-publisher-docker` process during the publish phase.

The primary documentation is in English. See the
[central German documentation](https://semrel.io/de/plugins/publishers/publisher-docker/).

## Prerequisites

- a `docker` CLI on `PATH`;
- a reachable Docker-compatible daemon containing the source image;
- authentication configured before semrel runs, for example with
  `docker login` or a CI credential helper.

The plugin never accepts credentials and never runs `docker login`. When
semrel itself runs in a container, the container needs a Docker CLI, the
daemon socket, and the relevant Docker authentication configuration. Access
to the Docker daemon socket is effectively root-equivalent host access; only
mount it into trusted jobs and containers.

## Installation

```bash
go install github.com/SemRels/publisher-docker/cmd/plugin@latest
mv "$(go env GOPATH)/bin/plugin" \
  "$HOME/.semrel/plugins/semrel-plugin-publisher-docker"
```

Released archives contain the platform binary. The published plugin container
also includes the Docker CLI, but still requires an explicitly mounted daemon
socket and authentication configuration.

## Configuration

```yaml
plugins:
  - uses: @semrel/publisher-docker
    phase: release
    args:
      image: acme-api:build
      ref: ghcr.io/acme/api:{version}
```

Build and authenticate before invoking semrel:

```bash
docker build --tag acme-api:build .
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io --username "$GHCR_USER" --password-stdin
semrel release
```

Semrel maps `args.image` and `args.ref` to the subprocess environment
variables shown below.

| Configuration / variable | Required | Description |
| --- | --- | --- |
| `image` / `SEMREL_PLUGIN_IMAGE` | Yes | Existing local image reference or image ID. |
| `ref` / `SEMREL_PLUGIN_REF` | Yes | Destination reference with an explicit tag and a `{version}` placeholder, for example `registry.example:5000/team/app:{version}`. Digest destinations are rejected. |
| `SEMREL_VERSION` | One version variable | Release version. One leading `v` is removed and SemVer `+` is encoded as `_` for the Docker tag. |
| `SEMREL_NEXT_VERSION` | Fallback | Used only when `SEMREL_VERSION` is empty. |
| `SEMREL_DRY_RUN` | No | `true` or `1` inspects both images and prints the plan without tagging or pushing. Defaults to `false`. |

All references are validated before Docker mutation. Empty values, control
characters such as CR/LF, unresolved placeholders, digest destinations, and
invalid Docker references fail closed.

## Publication behavior

1. Inspect the source image.
2. Inspect the destination tag and decide whether it must be created,
   replaced, or kept.
3. Tag only when the destination does not already identify the source image.
4. Inspect the destination again and verify its image ID.
5. Execute exactly one `docker image push`.
6. Report the immutable `repository@sha256:...` manifest reference obtained
   from the push result or the matching `RepoDigests` entry.

Example:

```text
publisher-docker: published ghcr.io/acme/api:1.4.0 as ghcr.io/acme/api@sha256:...
```

Docker command diagnostics are not relayed verbatim, preventing accidental
credential disclosure. Errors identify the failed stage, including missing
CLI, unavailable daemon, missing image, authentication, tagging, pushing, or
digest verification.

## Dry-run

Direct plugin dry-run still requires the source image and Docker daemon. It
performs source and destination inspection, then reports the exact tag/push
plan without creating or replacing a tag and without contacting the registry
with a push.

```bash
SEMREL_PLUGIN_IMAGE=acme-api:build \
SEMREL_PLUGIN_REF='ghcr.io/acme/api:{version}' \
SEMREL_VERSION=v1.4.0 \
SEMREL_DRY_RUN=true \
semrel-plugin-publisher-docker
```

Semrel core currently does not invoke release plugins during
`semrel release --dry-run`; run the plugin directly with
`SEMREL_DRY_RUN=true` when validating this publication plan.

## Explicit non-goals

The MVP does not build images, authenticate, sign, retry, manage tag policies,
create channel tags, or assemble multi-platform images/manifests.

## Development

```bash
go test ./...
go test -race ./...
go build ./cmd/plugin
golangci-lint run ./...
```

The Linux integration gate requires Docker and a local `registry:2.8.3`
service:

```bash
SEMREL_TEST_DOCKER_REGISTRY=localhost:5000 \
  go test -count=1 -tags=integration -timeout=5m ./cmd/plugin
```

When the integration tag is selected, missing prerequisites fail the test
rather than skipping it.

## License

Apache-2.0
