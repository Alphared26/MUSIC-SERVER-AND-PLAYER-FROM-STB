# Multi-stage build for ARM64 (HG680P Amlogic)
# Build on AMD64 laptop with QEMU binfmt, or native ARM64 if available.
# Usage:
#   docker buildx build --platform linux/arm64 -t flac-server .
#   # Or native (run on Armbian):
#   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o flac-server ./cmd/server
#   docker build -t flac-server .
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git build-base

WORKDIR /build

# Set target explicitly — works for cross-compile (QEMU) or native.
ARG TARGETOS=linux
ARG TARGETARCH=arm64

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -extldflags=-static" -o flac-server ./cmd/server

# Final stage: distroless ARM64
FROM gcr.io/distroless/static-debian12:nonroot-arm64

WORKDIR /

COPY --from=builder /build/flac-server /flac-server
COPY --from=builder /build/schema.sql /schema.sql
COPY --from=builder /build/config.yaml /config.yaml

EXPOSE 8080

ENV CONFIG_PATH=/config.yaml \
    GOGC=50 \
    GOMEMLIMIT=80MiB

USER nonroot:nonroot

ENTRYPOINT ["/flac-server"]