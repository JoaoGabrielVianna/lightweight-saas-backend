# syntax=docker/dockerfile:1.7

# --- Build stage ----------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Build metadata. Passed in rather than derived, because the build stage has no
# git: `docker build --build-arg VERSION=$(git describe --tags --always)`.
# Empty is fine — the binary reports "unknown" rather than failing to build.
ARG VERSION=""
ARG COMMIT=""

# Copy source and build a static binary.
#
# CGO_ENABLED=0 is what makes the result runnable on any base, and is why the
# runtime stage needs no libc. -trimpath keeps build-host paths out of panics,
# which would otherwise leak a directory layout into an error report.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/api ./cmd/api

# The migration CLI ships in the same image.
#
# DB_MIGRATE_ON_BOOT=false is a documented, supported choice — several replicas
# starting at once, or a runtime database role that may not DDL — and without
# this binary that choice has no path in a container deployment. An operator
# would have to build a second image or run migrations from a laptop, which is
# how a schema ends up applied from someone's checkout.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# --- Runtime stage --------------------------------------------------------
FROM alpine:3.20

# ca-certificates: the API makes outbound HTTPS calls to Keycloak, and without
#   these every one fails with an unhelpful x509 error.
# tzdata: nothing here assumes a timezone — timestamps are UTC — but Keycloak
#   returns local-time strings in some payloads, and a container with no
#   zoneinfo cannot parse them.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /out/migrate /app/migrate
# DEV-ONLY playground assets plus the admin console. Served only when their
# respective flags are on. Tiny (~12KB), so the same image works for local, dev
# and production rather than needing a variant per environment.
COPY web /app/web

# Non-root. The process binds 8080, which needs no privilege, and nothing here
# writes to disk.
USER app

# UTC everywhere, so a log line means the same thing regardless of where the
# container runs. Not "assumed" — stated, which is the point.
ENV TZ=UTC

EXPOSE 8080

# Readiness, run by the binary itself.
#
# Not curl or wget: alpine happens to ship wget today, and a healthcheck that
# depends on what the base image happens to include fails SILENTLY the day that
# changes — silently meaning the container reports unhealthy because the check
# could not run. The binary is already here and already knows its port.
#
# start-period covers migrations, which run at boot on the default settings.
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
    CMD ["/app/api", "-healthcheck"]

# Exec form, with no shell.
#
# This is what makes graceful shutdown work at all: the shell form would run the
# binary as a CHILD of /bin/sh, `docker stop` would deliver SIGTERM to the
# shell, and the API would never see it — it would be SIGKILLed after the grace
# period with every in-flight request dropped. In exec form the binary is PID 1
# and receives the signal directly.
ENTRYPOINT ["/app/api"]
