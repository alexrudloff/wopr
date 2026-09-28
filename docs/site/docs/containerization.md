# Containerization

WOPR runs with the permissions of its process. Use a container or virtual machine when you need an operating-system security boundary.

A container can limit filesystem, process, device, and network access. It does not make model output trustworthy.

## Build a WOPR image

WOPR has no published container image yet. Build from the source tree with a multi-stage Dockerfile:

```dockerfile
FROM golang:1.27.1 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /out/wopr ./cmd/wopr

FROM debian:bookworm-slim
RUN useradd --create-home --uid 10001 wopr
COPY --from=build /out/wopr /usr/local/bin/wopr
USER wopr
WORKDIR /workspace
ENTRYPOINT ["wopr"]
```

Pin base images by digest for a release build. Generate an SBOM and scan the final image before publication.

## Preserve state deliberately

WOPR stores user state below:

```text
~/.wopr
```

Mount a named volume when Sessions and settings must survive container replacement:

```bash
docker run --rm -it \
  -v wopr-home:/home/wopr/.wopr \
  -v "$PWD:/workspace" \
  wopr-local
```

Mounting your host `~/.wopr` gives the container access to host credentials, Sessions, and settings. Use a separate volume when that access is not required.

## Workspace access

Mount only the project paths that WOPR needs. Use a read-only mount when the agent should inspect but not modify files:

```bash
docker run --rm -it \
  -v "$PWD:/workspace:ro" \
  wopr-local -p "Review this repository"
```

A read-only workspace does not prevent access to another writable mount.

## Network access

Disable network access when the task does not need model or external service calls:

```bash
docker run --rm -it --network none wopr-local
```

Normal model inference requires network access unless the model endpoint is available through an explicitly permitted local network.

Use network policy or a proxy when the container must reach only approved endpoints.

## Secrets

Inject secrets at runtime. Do not bake them into an image or copy them into a build layer.

Use:

- environment variables;
- owner-only mounted files;

Review image history and generated SBOMs before release.

## Rootless execution

Run as a non-root user unless the task requires a specific privileged operation. Do not grant broad host mounts, Docker socket access, device access, or additional Linux capabilities by default.

## Related documentation

- [Security](security.md)
