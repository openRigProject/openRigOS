BOARD ?= rpizero2w
OUTPUT := $(PWD)/output
IMAGE_NAME := openrigos-builder

.PHONY: build image clean shell help

help:
	@echo "openRigOS Build System"
	@echo ""
	@echo "Targets:"
	@echo "  make image              Build the Docker builder image (run once)"
	@echo "  make build BOARD=<name> Build an OS image for a board"
	@echo "  make shell BOARD=<name> Drop into builder shell for debugging"
	@echo "  make clean              Remove build output"
	@echo ""
	@echo "Available boards:"
	@ls boards/
	@echo ""
	@echo "Examples:"
	@echo "  make image"
	@echo "  make build BOARD=rpizero2w"
	@echo "  make build BOARD=x86_64"

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
	docker run --rm -it --privileged \
	  $(DOCKER_VOLS) \
	  -e BOARD=$(BOARD) \
	  $(IMAGE_NAME) bash

clean:
	rm -rf $(OUTPUT)/*

_check_board:
	@test -d boards/$(BOARD) || (echo "ERROR: Board '$(BOARD)' not found in boards/" && exit 1)
