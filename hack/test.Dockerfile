# Base image for running the Go unit tests on Linux (see `make test-in-container`).
#
# It exists so contributors on macOS (or any non-Linux host) can exercise the
# cgo / Linux-only build paths -- e.g. the NVIDIA DCGM monitor -- without
# reinstalling the system build dependencies on every run. The image is built
# once (tagged by Go version) and reused; `make test-image FORCE=1` rebuilds it.
ARG GO_VERSION=latest
FROM golang:${GO_VERSION}

# The only system libraries the cgo build paths need: libsystemd (go-systemd)
# and pkg-config to locate it.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libsystemd-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
