# syntax=docker/dockerfile:1
#
# Leasorix container image: web UI (Node) -> binaries (Go) -> distroless.
#
#   docker build -f deploy/Dockerfile -t leasorix .      (or: make docker)
#
# The image runs as the unprivileged user "nonroot" (65532). leasorixd gets
# CAP_NET_BIND_SERVICE (DHCP port 67) and CAP_NET_RAW (raw sockets for
# L2 unicast replies and ICMP conflict detection) as file capabilities, so
# both must stay in the container's capability set (Docker's default keeps
# them; with cap_drop: [ALL] add them back). "no-new-privileges" is fine:
# file capabilities are then limited to that set, not removed. Base images
# can be replaced (e.g. by a registry mirror) with the build arguments below. Behind a TLS-inspecting proxy pass its CA as build
# secret: docker build --secret id=ca,src=proxy-ca.pem ...

ARG NODE_IMAGE=node:22-bookworm-slim
ARG GO_IMAGE=golang:1.27-bookworm
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=secret,id=ca,required=false \
    if [ -s /run/secrets/ca ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/ca; fi; \
    npm ci --no-audit --no-fund
COPY internal/api/openapi.yaml /src/internal/api/openapi.yaml
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=secret,id=ca,required=false \
    if [ -s /run/secrets/ca ]; then cat /etc/ssl/certs/ca-certificates.crt /run/secrets/ca > /tmp/ca.pem; export SSL_CERT_FILE=/tmp/ca.pem; fi; \
    go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -e; \
    LDFLAGS="-s -w -X github.com/meister119/leasorix/internal/buildinfo.Version=${VERSION} \
      -X github.com/meister119/leasorix/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/meister119/leasorix/internal/buildinfo.Date=${DATE}"; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags ui -trimpath -ldflags "$LDFLAGS" -o /out/usr/local/bin/leasorixd ./cmd/leasorixd; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "$LDFLAGS" -o /out/usr/local/bin/leasorixctl ./cmd/leasorixctl; \
    go run ./deploy/docker/setcap /out/usr/local/bin/leasorixd; \
    mkdir -p /out/data /out/certs; \
    chown 65532:65532 /out/data /out/certs

FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
LABEL org.opencontainers.image.title="Leasorix" \
      org.opencontainers.image.description="Self-hosted DHCP server with web UI and high availability" \
      org.opencontainers.image.source="https://github.com/meister119/leasorix" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="NOASSERTION"
COPY --from=build /out/usr/local/bin/ /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/certs /certs
COPY deploy/docker/leasorix.yml /etc/leasorix/leasorix.yml
ENV LEASORIX_CONFIG=/etc/leasorix/leasorix.yml
USER 65532:65532
VOLUME ["/data"]
EXPOSE 67/udp 8067/tcp 8068/tcp
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/usr/local/bin/leasorixd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/leasorixd"]
