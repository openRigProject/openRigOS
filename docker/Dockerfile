# Pin to amd64 — ensures consistent cross-compilation environment on
# all hosts (Intel Mac, Apple Silicon via Rosetta, Linux x86_64).
FROM --platform=linux/amd64 debian:bookworm

# Build tools
RUN apt-get update && apt-get install -y \
    golang-go \
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
    && rm -rf /var/lib/apt/lists/*

# Register QEMU binfmt handlers so we can run ARM64 binaries on x86
RUN update-binfmts --enable

WORKDIR /build

COPY scripts/ /build/scripts/
RUN chmod +x /build/scripts/*.sh
COPY overlay/ /build/overlay/
COPY src/ /build/src/

ENTRYPOINT ["/build/scripts/build.sh"]
