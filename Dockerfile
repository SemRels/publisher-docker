# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: 2026 The publisher-docker Authors

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" \
    -o /out/semrel-plugin-publisher-docker ./cmd/plugin

FROM docker:29.6.2-cli@sha256:be132a9f282288de4afaf63379dff75711fda0147c6b72a9df44e51841402144 AS docker-cli

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="SemRel publisher-docker plugin" \
      org.opencontainers.image.description="Publishes an existing local Docker image during a SemRel release" \
      org.opencontainers.image.source="https://github.com/SemRels/publisher-docker" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/semrel-plugin-publisher-docker /usr/local/bin/semrel-plugin-publisher-docker
USER nonroot
ENTRYPOINT ["/usr/local/bin/semrel-plugin-publisher-docker"]
