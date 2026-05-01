BOARD         ?= rpizero2w
HOST          ?= openrig-config.local
OUTPUT        := $(PWD)/output
IMAGE_NAME    := openrigos-builder
# Set UPDATE_CONFIG=1 to overwrite /etc/openrig.json on the device.
# Default (0) preserves the existing provisioned config.
UPDATE_CONFIG ?= 0
# Set DEPLOY_BINS=1 with deploy-overlay to also push already-built binaries
# from output/deploy-staging-<arch>/ without recompiling.
DEPLOY_BINS   ?= 0
# rsync flags for pushing to device:
#   --no-owner --no-group  — never apply Mac UID/GID to remote files
#   --rsync-path="sudo rsync" — run as root on device so we can write to /usr/local etc.
RSYNC_PUSH = rsync -avz --progress --no-owner --no-group --rsync-path="sudo rsync"

.PHONY: build image clean shell deploy deploy-overlay deploy-api deploy-web deploy-wasm deploy-display help

help:
	@echo "openRigOS Build System"
	@echo ""
	@echo "Targets:"
	@echo "  make image                        Build the Docker builder image (run once)"
	@echo "  make build BOARD=<name>           Build an OS image for a board"
	@echo "  make shell BOARD=<name>           Drop into builder shell for debugging"
	@echo "  make deploy HOST=<host>                        Build Go/WASM + overlay and deploy over SSH"
	@echo "  make deploy HOST=<host> UPDATE_CONFIG=1        Also overwrite /etc/openrig.json"
	@echo "  make deploy-overlay HOST=<host>                          Deploy overlay files only (no recompile)"
	@echo "  make deploy-overlay HOST=<host> DEPLOY_BINS=1           Also push already-built binaries"
	@echo "  make deploy-overlay HOST=<host> UPDATE_CONFIG=1         Also overwrite /etc/openrig.json"
	@echo "  make deploy-api HOST=<host>                            Cross-compile and deploy only openrig-api + WASM (fast)"
	@echo "  make deploy-web HOST=<host>                            Cross-compile and deploy only openrig-provision-web + WASM (fast)"
	@echo "  make clean                        Remove build output"
	@echo ""
	@echo "Available boards:"
	@ls boards/
	@echo ""
	@echo "Examples:"
	@echo "  make image"
	@echo "  make build BOARD=rpizero2w"
	@echo "  make build BOARD=x86_64"
	@echo "  make deploy HOST=kc1ygy-hotspot.local"
	@echo "  make deploy-overlay HOST=kc1ygy-hotspot.local"
	@echo "  make deploy-overlay HOST=kc1ygy-hotspot.local UPDATE_CONFIG=1"

image:
	docker build -t $(IMAGE_NAME) -f docker/Dockerfile .

DOCKER_VOLS = \
	-v $(OUTPUT):/output \
	-v $(PWD)/boards:/build/boards \
	-v $(PWD)/packages:/build/packages \
	-v $(PWD)/scripts:/build/scripts \
	-v $(PWD)/overlay:/build/overlay \
	-v $(PWD)/src:/build/src

build: _check_board
	@mkdir -p $(OUTPUT)
	docker run --rm --privileged \
	  $(DOCKER_VOLS) \
	  -e BOARD=$(BOARD) \
	  $(IMAGE_NAME)

shell: _check_board
	@mkdir -p $(OUTPUT)
	docker run --rm -it --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -e BOARD=$(BOARD) \
	  $(IMAGE_NAME)

clean:
	rm -rf $(OUTPUT)/*

# Build Go binaries + WASM in Docker (cross-compiling for the remote host's arch),
# then deploy to device over SSH.  Architecture is auto-detected via 'uname -m'.
# Usage:
#   make deploy HOST=openrig-config.local
#   make deploy HOST=kc1ygy-hotspot.local
deploy:
	@REMOTE_ARCH=$$(ssh openrig@$(HOST) "uname -m") && \
	case "$$REMOTE_ARCH" in \
	  aarch64) GOARCH=arm64 ; DEBIAN_ARCH=arm64 ;; \
	  x86_64)  GOARCH=amd64 ; DEBIAN_ARCH=amd64 ;; \
	  armv7l)  GOARCH=arm   ; DEBIAN_ARCH=armhf  ;; \
	  *) echo "ERROR: Unknown remote arch: $$REMOTE_ARCH" && exit 1 ;; \
	esac && \
	STAGING=$(OUTPUT)/deploy-staging-$$GOARCH && \
	mkdir -p $$STAGING && \
	echo "==> Detected $$REMOTE_ARCH → GOARCH=$$GOARCH DEBIAN_ARCH=$$DEBIAN_ARCH" && \
	echo "==> Compiling Go binaries + WASM..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e GOARCH=$$GOARCH \
	  $(IMAGE_NAME) scripts/00-gobuilds.sh && \
	echo "==> Cross-compiling MMDVMHost..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e DEBIAN_ARCH=$$DEBIAN_ARCH \
	  $(IMAGE_NAME) scripts/00-mmdvmhost.sh && \
	echo "==> Cross-compiling DMRGateway + YSFGateway..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e DEBIAN_ARCH=$$DEBIAN_ARCH \
	  $(IMAGE_NAME) scripts/01-gateways.sh && \
	echo "==> Deploying native binaries to $(HOST):/usr/local/bin/ ..." && \
	$(RSYNC_PUSH) \
	  --exclude='openrig.wasm' --exclude='wasm_exec.js' \
	  $$STAGING/ \
	  openrig@$(HOST):/usr/local/bin/ && \
	echo "==> Deploying WASM assets to $(HOST):/usr/local/lib/openrig/ ..." && \
	$(RSYNC_PUSH) \
	  $$STAGING/openrig.wasm \
	  $$STAGING/wasm_exec.js \
	  openrig@$(HOST):/usr/local/lib/openrig/ && \
	echo "==> Deploying overlay (UPDATE_CONFIG=$(UPDATE_CONFIG))..." && \
	$(RSYNC_PUSH) \
	  --exclude='/home/' \
	  $(if $(filter 0,$(UPDATE_CONFIG)),--exclude='/etc/openrig.json',) \
	  overlay/common/ \
	  openrig@$(HOST):/ && \
	echo "==> Fixing permissions on $(HOST)..." && \
	ssh openrig@$(HOST) "sudo chmod +x /usr/local/lib/openrig/*.sh /usr/local/bin/openrig-* 2>/dev/null; sudo chown -R openrig:openrig /etc/mmdvm /etc/dmrgateway /etc/ysfgateway 2>/dev/null; sudo chown openrig:openrig /usr/local/etc/YSFHosts.json /usr/local/etc/FCSHosts.txt 2>/dev/null; sudo mkdir -p /var/log/openrig && sudo chown openrig:openrig /var/log/openrig; true" && \
	echo "==> Reloading systemd and restarting services on $(HOST)..." && \
	ssh openrig@$(HOST) "sudo systemctl daemon-reload && sudo systemctl restart openrig-provision-web.service openrig-api.service openrig-rigctld.service" && \
	echo "==> Regenerating MMDVM/gateway configs from openrig.json..." && \
	ssh openrig@$(HOST) "sudo /usr/local/lib/openrig/mmdvm-update.sh || true" && \
	echo "==> Deploy complete."

# Deploy only overlay files (configs, scripts, service units) to device over SSH.
# No recompilation — fast iteration on shell scripts, config templates, service files.
# Usage:
#   make deploy-overlay HOST=openrig-config.local
deploy-overlay:
	@echo "==> Deploying overlay to $(HOST) (UPDATE_CONFIG=$(UPDATE_CONFIG) DEPLOY_BINS=$(DEPLOY_BINS))..."
	$(RSYNC_PUSH) \
	  --exclude='/home/' \
	  $(if $(filter 0,$(UPDATE_CONFIG)),--exclude='/etc/openrig.json',) \
	  overlay/common/ \
	  openrig@$(HOST):/
	@if [ "$(DEPLOY_BINS)" = "1" ]; then \
	  REMOTE_ARCH=$$(ssh openrig@$(HOST) "uname -m") && \
	  case "$$REMOTE_ARCH" in \
	    aarch64) GOARCH=arm64 ;; \
	    x86_64)  GOARCH=amd64 ;; \
	    armv7l)  GOARCH=arm   ;; \
	    *) echo "ERROR: Unknown remote arch: $$REMOTE_ARCH" && exit 1 ;; \
	  esac && \
	  STAGING=$(OUTPUT)/deploy-staging-$$GOARCH && \
	  test -d "$$STAGING" || (echo "ERROR: No staged binaries found at $$STAGING — run 'make deploy' first" && exit 1) && \
	  echo "==> Deploying binaries from $$STAGING ..." && \
	  $(RSYNC_PUSH) \
	    --exclude='openrig.wasm' --exclude='wasm_exec.js' \
	    $$STAGING/ \
	    openrig@$(HOST):/usr/local/bin/ && \
	  $(RSYNC_PUSH) \
	    $$STAGING/openrig.wasm \
	    $$STAGING/wasm_exec.js \
	    openrig@$(HOST):/usr/local/lib/openrig/; \
	fi
	@echo "==> Fixing permissions on $(HOST)..."
	ssh openrig@$(HOST) "sudo chmod +x /usr/local/lib/openrig/*.sh /usr/local/bin/openrig-* 2>/dev/null || true"
	ssh openrig@$(HOST) "sudo chown -R openrig:openrig /etc/mmdvm /etc/dmrgateway /etc/ysfgateway 2>/dev/null || true"
	ssh openrig@$(HOST) "sudo chown openrig:openrig /usr/local/etc/YSFHosts.json /usr/local/etc/FCSHosts.txt 2>/dev/null || true"
	ssh openrig@$(HOST) "sudo mkdir -p /var/log/openrig && sudo chown openrig:openrig /var/log/openrig"
	$(MAKE) deploy-wasm HOST=$(HOST)
	@echo "==> Reloading systemd and restarting services on $(HOST)..."
	ssh openrig@$(HOST) "sudo systemctl daemon-reload && sudo systemctl restart openrig-provision-web.service openrig-api.service openrig-rigctld.service"
	@echo "==> Regenerating MMDVM/gateway configs from openrig.json..."
	ssh openrig@$(HOST) "sudo /usr/local/lib/openrig/mmdvm-update.sh || true"
	@echo "==> Overlay deploy complete."

# Cross-compile and deploy only openrig-api to the device.
# Much faster than 'make deploy' — skips MMDVMHost and gateway builds.
# Usage:
#   make deploy-api HOST=kc1ygy-hotspot.local
deploy-api:
	@REMOTE_ARCH=$$(ssh openrig@$(HOST) "uname -m") && \
	case "$$REMOTE_ARCH" in \
	  aarch64) GOARCH=arm64 ;; \
	  x86_64)  GOARCH=amd64 ;; \
	  armv7l)  GOARCH=arm   ;; \
	  *) echo "ERROR: Unknown remote arch: $$REMOTE_ARCH" && exit 1 ;; \
	esac && \
	STAGING=$(OUTPUT)/deploy-staging-$$GOARCH && \
	mkdir -p $$STAGING && \
	echo "==> Detected $$REMOTE_ARCH → GOARCH=$$GOARCH" && \
	echo "==> Cross-compiling openrig-api..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e GOARCH=$$GOARCH \
	  -e ONLY_API=1 \
	  $(IMAGE_NAME) scripts/00-gobuilds.sh && \
	echo "==> Deploying openrig-api to $(HOST)..." && \
	$(RSYNC_PUSH) $$STAGING/openrig-api openrig@$(HOST):/usr/local/bin/openrig-api && \
	$(MAKE) deploy-wasm HOST=$(HOST) && \
	echo "==> Restarting openrig-api on $(HOST)..." && \
	ssh openrig@$(HOST) "sudo systemctl restart openrig-api.service" && \
	echo "==> deploy-api complete."

# Cross-compile and deploy only openrig-provision-web to the device.
# Usage:
#   make deploy-web HOST=kc1ygy-hotspot.local
deploy-web:
	@REMOTE_ARCH=$$(ssh openrig@$(HOST) "uname -m") && \
	case "$$REMOTE_ARCH" in \
	  aarch64) GOARCH=arm64 ;; \
	  x86_64)  GOARCH=amd64 ;; \
	  armv7l)  GOARCH=arm   ;; \
	  *) echo "ERROR: Unknown remote arch: $$REMOTE_ARCH" && exit 1 ;; \
	esac && \
	STAGING=$(OUTPUT)/deploy-staging-$$GOARCH && \
	mkdir -p $$STAGING && \
	echo "==> Detected $$REMOTE_ARCH → GOARCH=$$GOARCH" && \
	echo "==> Cross-compiling openrig-provision-web..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e GOARCH=$$GOARCH \
	  -e ONLY_WEB=1 \
	  $(IMAGE_NAME) scripts/00-gobuilds.sh && \
	echo "==> Deploying openrig-provision-web to $(HOST)..." && \
	$(RSYNC_PUSH) $$STAGING/openrig-provision-web openrig@$(HOST):/usr/local/bin/openrig-provision-web && \
	$(MAKE) deploy-wasm HOST=$(HOST) && \
	echo "==> deploy-web complete."

# Build and deploy only the WASM client (openrig.wasm + wasm_exec.js).
# Usage:
#   make deploy-wasm HOST=kc1ygy-hotspot.local
deploy-display:
	@REMOTE_ARCH=$$(ssh openrig@$(HOST) "uname -m") && \
	case "$$REMOTE_ARCH" in \
	  aarch64) GOARCH=arm64 ;; \
	  x86_64)  GOARCH=amd64 ;; \
	  armv7l)  GOARCH=arm   ;; \
	  *) echo "ERROR: Unknown remote arch: $$REMOTE_ARCH" && exit 1 ;; \
	esac && \
	STAGING=$(OUTPUT)/deploy-staging-$$GOARCH && \
	mkdir -p $$STAGING && \
	echo "==> Cross-compiling openrig-display..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e GOARCH=$$GOARCH \
	  -e ONLY_DISPLAY=1 \
	  $(IMAGE_NAME) scripts/00-gobuilds.sh && \
	echo "==> Deploying openrig-display to $(HOST)..." && \
	$(RSYNC_PUSH) $$STAGING/openrig-display openrig@$(HOST):/usr/local/bin/openrig-display && \
	$(MAKE) deploy-wasm HOST=$(HOST) && \
	echo "==> deploy-display complete."

deploy-wasm:
	@STAGING=$(OUTPUT)/deploy-staging-wasm && \
	mkdir -p $$STAGING && \
	echo "==> Building WASM..." && \
	docker run --rm --privileged --entrypoint bash \
	  $(DOCKER_VOLS) \
	  -v $$STAGING:/tmp/openrigos-bins \
	  -e ONLY_WASM=1 \
	  $(IMAGE_NAME) scripts/00-gobuilds.sh && \
	echo "==> Deploying WASM to $(HOST):/usr/local/lib/openrig/ ..." && \
	$(RSYNC_PUSH) \
	  $$STAGING/openrig.wasm \
	  $$STAGING/wasm_exec.js \
	  openrig@$(HOST):/usr/local/lib/openrig/ && \
	echo "==> Restarting openrig-provision-web on $(HOST)..." && \
	ssh openrig@$(HOST) "sudo systemctl restart openrig-provision-web.service" && \
	echo "==> deploy-wasm complete."

_check_board:
	@test -d boards/$(BOARD) || (echo "ERROR: Board '$(BOARD)' not found in boards/" && exit 1)
