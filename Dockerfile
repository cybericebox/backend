# syntax=mirror.gcr.io/docker/dockerfile:1
# AP Backend — pure auth/API service. Same build shape as the legacy daemon's
# deploy/Dockerfile (static binary + migrations copied into a slim alpine image).
# Migrations run on boot via golang-migrate from file://migrations, so the
# migrations directory must sit next to the binary at WORKDIR (/app/migrations).
# Boot uses ENV != "development" so the in-app MigrationsPath resolves to
# "migrations" (relative to /app), not the source-tree path.
# The build runs on the build platform and cross-compiles (pure Go, CGO off): arm64 images need no emulation.
ARG LABORATORY_CONTRACT_SOURCE=1f8d6885654c19b040137ffd2a14df9f99b0a9f5
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
ARG LABORATORY_CONTRACT_SOURCE
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /build
COPY go.mod go.sum ./
# Resolve the published Laboratory release pinned in go.mod.
RUN GOWORK=off go mod download
COPY . .
COPY ./internal/delivery/repository/postgres/migrations /build/migrations
RUN GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags '-s -w' -o app ./cmd/daemon
# Operator tool for load tests (docs/loadtest/RUNBOOK.md): creates and removes marked test accounts. Not run by the service.
RUN GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags '-s -w' -o loadtest-users ./cmd/loadtest-users

FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
# Runtime Laboratory distribution identity stays separate from SDK source.
ARG LABORATORY_VERSION=unknown
LABEL org.cybericebox.laboratory.commit=$LABORATORY_VERSION
ARG LABORATORY_CONTRACT_SOURCE
LABEL org.cybericebox.laboratory.contract.source=$LABORATORY_CONTRACT_SOURCE
# The daemon runs as an unprivileged user (UID 10001, no root inside the container; the numeric USER needs no passwd
# entry). Without TLS material it listens on plain 8080 (HTTP_SERVER_PORT changes it). With /tls/tls.crt and
# /tls/tls.key mounted it serves HTTPS on 8443 only, and with /aop/ca.crt too it requires client certificates:
# the deploy passes none of these. The health listener is on 8081.
ENV HTTP_SERVER_HEALTH_PORT=8081
WORKDIR /app

# db migration files (applied on boot)
COPY --from=builder /build/migrations /app/migrations

# the built binary
COPY --from=builder /build/app /app/app
COPY --from=builder /build/loadtest-users /app/loadtest-users

USER 10001:10001
ENTRYPOINT ["/app/app"]
# 8080: plain HTTP (no TLS files); 8443: HTTPS (/tls/tls.crt, /tls/tls.key); 8081: the health port (HTTP_SERVER_HEALTH_PORT).
EXPOSE 8080 8443 8081
