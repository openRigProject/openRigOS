# Pin to amd64 — ensures consistent cross-compilation environment on
# all hosts (Intel Mac, Apple Silicon via Rosetta, Linux x86_64).
ARG BUILDPLATFORM=linux/amd64
FROM --platform=${BUILDPLATFORM} debian:bookworm

# Add foreign architectures for cross-compilation of C/C++ libraries
RUN dpkg --add-architecture arm64 && \
    dpkg --add-architecture armhf

# Build tools
RUN apt-get update && apt-get install -y \
    debootstrap \
    qemu-user-static \
    binfmt-support \
    squashfs-tools \
    xorriso \
    grub-efi-amd64-bin \
    grub-pc-bin \
    dosfstools \
    parted \
    kpartx \
    rsync \
    wget \
    curl \
    gnupg \
    ca-certificates \
    zip \
    unzip \
    jq \
    git \
    build-essential \
    g++-aarch64-linux-gnu \
    g++-arm-linux-gnueabihf \
    nlohmann-json3-dev \
    libmosquitto-dev \
    libmosquitto-dev:arm64 \
    libmosquitto-dev:armhf \
    && rm -rf /var/lib/apt/lists/*

# Install Go 1.25.5 from official source (Debian bookworm ships Go 1.19 which is too old)
RUN wget -q https://go.dev/dl/go1.25.5.linux-amd64.tar.gz -O /tmp/go.tar.gz \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# Register QEMU binfmt handlers so we can run ARM64 binaries on x86
RUN update-binfmts --enable

WORKDIR /build

COPY scripts/ /build/scripts/
RUN chmod +x /build/scripts/*.sh
COPY overlay/ /build/overlay/
COPY src/ /build/src/

ENTRYPOINT ["/build/scripts/build.sh"]
