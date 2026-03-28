FROM debian:bookworm

# Build tools
RUN apt-get update && apt-get install -y \
    debootstrap \
    qemu-user-static \
    binfmt-support \
    squashfs-tools \
    xorriso \
    grub-efi-amd64-bin \
    grub-efi-arm64-bin \
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
    && rm -rf /var/lib/apt/lists/*

# Register QEMU binfmt handlers so we can run ARM64 binaries on x86
RUN update-binfmts --enable

WORKDIR /build

COPY scripts/ /build/scripts/
RUN chmod +x /build/scripts/*.sh

ENTRYPOINT ["/build/scripts/build.sh"]
