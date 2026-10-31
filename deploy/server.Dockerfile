# syntax=docker/dockerfile:1
# srvmon-server: web UI + API in one static binary on distroless.
# Build stages run on the build platform and cross-compile, so multi-arch
# images do not need QEMU.

FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/srvmon-server ./cmd/server \
 && mkdir -p /out/data /out/log

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/srvmon-server /srvmon-server
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/log /var/log/srvmon
ENV SRVMON_LISTEN=:8080 \
    SRVMON_DATA_DIR=/data \
    SRVMON_AUTH_LOG=/var/log/srvmon/auth.log
USER nonroot:nonroot
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/srvmon-server", "healthcheck"]
ENTRYPOINT ["/srvmon-server"]
CMD ["serve"]
