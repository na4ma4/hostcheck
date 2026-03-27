# syntax=docker/dockerfile:1
# Dockerfile for HostCheck with plugin support
#
# Builds the main binary and plugins directly (no mage dependency in Docker)
# Supports multi-platform builds (linux/amd64, linux/arm64)
#
# IMPORTANT: CGO plugins require native compilation, so we build on the target platform
# (not cross-compile). This means docker will use QEMU emulation or native builder nodes.
#
# Build single platform:
#   docker buildx build --platform linux/amd64 -t hostcheck .
#
# Build multi-platform (requires QEMU or multi-node builder):
#   docker buildx build --platform linux/amd64,linux/arm64 -t hostcheck .
#
# Setup QEMU for multi-platform:
#   docker run --privileged --rm tonistiigi/binfmt --install all

#############################################
# Build stage - builds on TARGET platform (not cross-compile)
# Required for CGO plugins which are architecture-specific
#############################################
FROM golang:1.26-trixie AS builder

# Build arguments for cache naming
ARG TARGETOS
ARG TARGETARCH

# Install build dependencies (gcc for CGO/plugin support)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    gcc \
    libc6-dev \
    git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Set environment variables for Go cache
ENV GOMODCACHE=/go/pkg/mod \
    GOCACHE=/root/.cache/go-build \
    CGO_ENABLED=1

# Copy go mod files first
COPY go.mod go.sum go.work go.work.sum ./

# Copy plugin go mod files
COPY plugins/dns/go.mod plugins/dns/go.sum ./plugins/dns/

# Copy plugin go mod files
COPY magefiles/go.mod magefiles/go.sum ./magefiles/

# Download dependencies
RUN GOTOOLCHAIN=auto go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/
COPY plugins/ ./plugins/

COPY build/ /build/

RUN /build/build-all.sh

#############################################
# Final stage - distroless with glibc
#############################################
FROM gcr.io/distroless/cc-debian12:nonroot

ARG TARGETOS
ARG TARGETARCH

# Copy CA certificates and timezone data
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /src/artifacts/build/release/${TARGETOS}/${TARGETARCH}/hostcheck /hostcheck

# Copy plugins
COPY --from=builder /src/artifacts/build/release/${TARGETOS}/${TARGETARCH}/plugins/ /plugins/

# Use nonroot user (uid 65532) for least privilege
USER nonroot:nonroot

# Expose the default port
EXPOSE 8080

# Environment variables
ENV LISTEN=:8080
ENV PLUGINS_DIR=/plugins

# Health check using the healthcheck subcommand
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/hostcheck", "healthcheck"]

# Run the binary
ENTRYPOINT ["/hostcheck"]
