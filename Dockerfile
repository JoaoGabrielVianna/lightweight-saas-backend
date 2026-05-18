# syntax=docker/dockerfile:1.7

# --- Build stage ----------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source and build a static binary.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# --- Runtime stage --------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=build /out/api /app/api
# DEV-ONLY playground assets. Served at /dev/auth IFF DEV_PLAYGROUND_ENABLED=true.
# Tiny (~12KB total). Including them unconditionally so the same image can be
# used in local + dev + staging while still gating exposure via env flag.
COPY web /app/web

USER app
EXPOSE 8080

ENTRYPOINT ["/app/api"]
