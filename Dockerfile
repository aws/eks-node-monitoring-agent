# EKS Node Monitoring Agent Dockerfile
#
# Build: docker build -t eks-node-monitoring-agent .
# Run:   docker run --privileged eks-node-monitoring-agent

# =============================================================================
# Stage 1: Amazon Linux builder for systemd libraries
# =============================================================================
FROM public.ecr.aws/amazonlinux/amazonlinux:2023 AS systemd-builder

RUN dnf install -y systemd-devel && \
    dnf clean all

# =============================================================================
# Stage 2: DCGM builder for GPU monitoring libraries and the DCGM host engine
# =============================================================================
# NOTE: do not add --platform to this stage. The repository below is selected
# from `uname -m`, so building on the build platform instead of the target
# platform would silently place amd64 binaries in the arm64 image.
FROM public.ecr.aws/amazonlinux/amazonlinux:2023 AS dcgm-builder

# DCGM version served to GPU nodes. Pinned for reproducibility: this determines
# the nv-hostengine version the agent talks to, not just a client library.
ARG DCGM_VERSION=4.6.1-1

# Install DCGM from the NVIDIA repository for GPU monitoring support.
# This is optional - the agent works without it on non-GPU nodes.
#   -core:        libdcgm client, DCGM modules, nv-hostengine, dcgmi, nvvs
#   -proprietary: plugins/cudaless/libEud.so, for DCGM diagnostics
# The CUDA plugin tiers (-cudaXX, -proprietary-cudaXX) are deliberately not
# installed: they add >1GB and the dcgm-exporter image previously used here did
# not ship them either, so DCGM diagnostics keep the same cudaless-only scope.
RUN dnf install -y dnf-plugins-core && \
    dnf config-manager --add-repo https://developer.download.nvidia.com/compute/cuda/repos/rhel9/$(uname -m | sed -e 's/aarch64/sbsa/')/cuda-rhel9.repo && \
    dnf install -y \
        datacenter-gpu-manager-4-core-1:${DCGM_VERSION} \
        datacenter-gpu-manager-4-proprietary-1:${DCGM_VERSION} \
        datacenter-gpu-manager-exporter && \
    dnf clean all

# Stage the runtime payload into a single tree. `cp -a` preserves the
# libfoo.so.4 -> libfoo.so.4.x.y symlinks; copying the globs directly would
# dereference them and store every library twice.
# dcgm-exporter and its default counter CSVs ship too: they are only executed
# when the chart's opt-in metrics endpoint is enabled, but the binary must be
# present in the image for that flag to work.
RUN mkdir -p /staging/usr/lib64 /staging/usr/bin /staging/usr/libexec /staging/etc/dcgm-exporter && \
    cp -a /usr/lib64/libdcgm.so* /usr/lib64/libdcgmmodule*.so* /staging/usr/lib64/ && \
    cp -a /usr/bin/nv-hostengine /usr/bin/dcgmi /usr/bin/dcgm-exporter /staging/usr/bin/ && \
    cp -a /usr/libexec/datacenter-gpu-manager-4 /staging/usr/libexec/ && \
    cp -a /etc/dcgm-exporter/. /staging/etc/dcgm-exporter/

# =============================================================================
# Stage 2b: BusyBox — a single static shell for the opt-in metrics launcher
# =============================================================================
# The distroless runtime base ships no shell. The opt-in DCGM metrics command
# (chart: dcgmAgent.metrics.enabled) needs one to run dcgm-exporter in the
# background while nv-hostengine stays PID 1. The uclibc BusyBox image is a
# single fully static binary, so it adds a shell without pulling glibc/bash and
# their libraries into the image.
ARG BUSYBOX_VERSION=1.37.0
FROM public.ecr.aws/docker/library/busybox:${BUSYBOX_VERSION}-uclibc AS busybox

# =============================================================================
# Stage 3: Go builder to compile the application
# =============================================================================
FROM public.ecr.aws/docker/library/golang:1.26.6 AS go-builder

WORKDIR /workspace

# Install build dependencies for CGO (systemd bindings)
RUN apt-get update && apt-get install -y libsystemd-dev gcc && \
    rm -rf /var/lib/apt/lists/*

# Cache Go module dependencies
COPY go.mod go.sum ./
RUN GOPROXY=direct go mod download

# Copy source code
COPY . .

# Build arguments for flexible Go build configuration
ARG TARGETOS=linux
ARG TARGETARCH
ARG GOBUILDARGS=""
ARG VERSION=build
ARG GIT_COMMIT=unknown

# Build the agent and chroot binaries with CGO enabled (required for systemd) and goroutine leak profiling
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOEXPERIMENT=goroutineleakprofile \
    go build ${GOBUILDARGS} \
        -ldflags="-s -w \
            -X github.com/aws/eks-node-monitoring-agent/internal/version.Version=${VERSION} \
            -X github.com/aws/eks-node-monitoring-agent/internal/version.GitCommit=${GIT_COMMIT}" \
        -o bin/eks-node-monitoring-agent ./cmd/eks-node-monitoring-agent/
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOEXPERIMENT=goroutineleakprofile \
    go build ${GOBUILDARGS} -ldflags="-s -w" -o bin/chroot ./cmd/chroot/

# =============================================================================
# Stage 4: Minimal runtime image
# =============================================================================
FROM public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base-glibc:latest-al23 AS runtime

# Labels for container metadata
LABEL org.opencontainers.image.title="EKS Node Monitoring Agent"
LABEL org.opencontainers.image.description="Kubernetes node monitoring agent for health checks and diagnostics"
LABEL org.opencontainers.image.source="https://github.com/aws/eks-node-monitoring-agent"
LABEL org.opencontainers.image.vendor="Amazon Web Services"

# Copy systemd libraries from builder (required for journald integration)
COPY --from=systemd-builder /usr/lib64/libsystemd.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/liblz4.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/liblzma.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/libzstd.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/libgcrypt.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/libgpg-error.so* /usr/lib64/
COPY --from=systemd-builder /usr/lib64/libcap.so* /usr/lib64/

# Copy the DCGM client library, DCGM modules, nv-hostengine, dcgmi and nvvs.
# The agent itself only needs libdcgm (and only on GPU nodes), but the chart's
# dcgm-server DaemonSet runs nv-hostengine out of this same image, which is why
# the host engine and its modules ship here instead of in a separate image.
# Kept above the binary copies below so agent code changes do not invalidate it.
COPY --from=dcgm-builder /staging/ /

# Static BusyBox providing /usr/bin/sh and /usr/bin/sleep for the opt-in DCGM
# metrics launcher only. Nothing runs it unless dcgmAgent.metrics.enabled is set.
COPY --from=busybox /bin/busybox /usr/bin/busybox
RUN ["/usr/bin/busybox", "ln", "-s", "/usr/bin/busybox", "/usr/bin/sh"]
RUN ["/usr/bin/busybox", "ln", "-s", "/usr/bin/busybox", "/usr/bin/sleep"]

# Copy the built binaries
COPY --from=go-builder /workspace/bin/eks-node-monitoring-agent /opt/bin/eks-node-monitoring-agent
COPY --from=go-builder /workspace/bin/chroot /opt/bin/chroot

# Set working directory
WORKDIR /opt/bin

# No USER is set: the agent needs root and privileged host access (journald, host mounts, chroot),
# which is granted via the Helm chart securityContext (privileged: true).

ENTRYPOINT ["/opt/bin/eks-node-monitoring-agent"]
