# openRigOS

Custom Linux distribution (Debian-based) for ham radio hardware. Supports hotspot controllers, remote radio CAT control, and console workstations on Raspberry Pi Zero 2W, RPi4, RPi5, and x86_64.

> **PRE-RELEASE SOFTWARE — EXPERIMENTAL USE ONLY**
>
> This project is pre-release software. It has not been tested for security, reliability, or fitness for any particular purpose. Use it only for experimentation and personal learning. It is **not** suitable for production or safety-critical use.
>
> By using this software you agree that the author(s) shall not be held liable for any damages, data loss, security incidents, regulatory violations, or any other harm arising from its use.

## Features

- First-boot provisioning wizard via USB gadget or `http://<hostname>.local`
- Always-on web management UI at port 80
- ConnectRPC API (port 7373) for rig control, hotspot config, WiFi management
- MMDVMHost + DMRGateway + YSFGateway for MMDVM_HS_Hat hotspots
- hamlib `rigctld` daemon (port 4532) for CAT control
- mDNS/DNS-SD discovery via Avahi

## Building

Requires Docker.

```bash
# Raspberry Pi Zero 2W (default)
make build BOARD=rpizero2w

# Raspberry Pi 4
make build BOARD=rpi4

# Raspberry Pi 5
make build BOARD=rpi5

# x86_64
make build BOARD=x86_64
```

Output: `output/openRigOS-<board>-<arch>.img`

## Flashing

Use [Raspberry Pi Imager](https://www.raspberrypi.com/software/) or `dd`:

```bash
xz -d openRigOS-rpizero2w-arm64.img.xz
sudo dd if=openRigOS-rpizero2w-arm64.img of=/dev/sdX bs=4M status=progress
```

## First Boot

1. Connect via USB — device appears as `openrig.local` (USB gadget Ethernet)
2. Browse to `http://openrig.local` to run the provisioning wizard
3. After provisioning, the device connects to your WiFi network
