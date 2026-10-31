# syntax=docker/dockerfile:1
# srvmon-agent: tiny static binary. Runs as root inside the container only to
# read the docker socket and host /proc (both mounted read-only).

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/agent ./cmd/agent
COPY internal/agent ./internal/agent
COPY internal/protocol ./internal/protocol
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/srvmon-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/srvmon-agent /srvmon-agent
ENV HOST_PROC=/host/proc HOST_ROOT=/host DOCKER_SOCK=/var/run/docker.sock
ENTRYPOINT ["/srvmon-agent"]
