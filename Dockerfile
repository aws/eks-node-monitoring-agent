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
# Stage 2: DCGM builder for GPU monitoring libraries
# =============================================================================
FROM public.ecr.aws/amazonlinux/amazonlinux:2023 AS dcgm-builder

# Install DCGM from NVIDIA repository for GPU monitoring support
# This is optional - the agent works without it on non-GPU nodes
RUN dnf install -y dnf-plugins-core && \
    dnf config-manager --add-repo https://developer.download.nvidia.com/compute/cuda/repos/rhel9/$(uname -m | sed -e 's/aarch64/sbsa/')/cuda-rhel9.repo && \
    dnf install -y datacenter-gpu-manager-4-core && \
    dnf clean all

# =============================================================================
# Stage 3: Go builder to compile the application
# =============================================================================
FROM public.ecr.aws/docker/library/golang:1.25.5 AS go-builder

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

# Build the binary with CGO enabled (required for systemd) and greenteagc experiment
# Note: The cmd/ directory will be added when the core monitoring framework is migrated
RUN if [ -d "./cmd/eks-node-monitoring-agent" ]; then \
        CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOEXPERIMENT=greenteagc \
        go build ${GOBUILDARGS} -ldflags="-s -w" -o bin/eks-node-monitoring-agent ./cmd/eks-node-monitoring-agent/; \
    else \
        echo "No cmd/eks-node-monitoring-agent found - creating placeholder binary"; \
        mkdir -p bin && \
        echo '#!/bin/sh' > bin/eks-node-monitoring-agent && \
        echo 'echo "EKS Node Monitoring Agent - binary not yet available"' >> bin/eks-node-monitoring-agent && \
        echo 'echo "The core monitoring framework has not been migrated yet."' >> bin/eks-node-monitoring-agent && \
        chmod +x bin/eks-node-monitoring-agent; \
    fi

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

# Copy DCGM client library for GPU monitoring (optional - only used on GPU nodes)
COPY --from=dcgm-builder /usr/lib64/libdcgm.so* /usr/lib64/

# Copy the built binary (if it exists)
COPY --from=go-builder /workspace/bin/eks-node-monitoring-agent /opt/bin/eks-node-monitoring-agent

# Set working directory
WORKDIR /opt/bin

# Run as non-root user (the agent will use privileged container settings for host access)
# Note: Some operations require privileged mode, configured via Helm chart securityContext

ENTRYPOINT ["/opt/bin/eks-node-monitoring-agent"]
