# Build, test and deploy the Raspberry Pi services.
#
#   make test               run unit tests
#   make dist               cross-compile for the Pi 4 (arm64) and build a tarball
#   make install HOST=root@10.41.113.1
#                           copy the tarball to a radio and (re)start the services

GO      ?= go
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUILD   := build
ROOTFS  := $(BUILD)/rootfs
TARBALL := $(BUILD)/rar-radio-$(VERSION)-arm64.tar.gz
CMDS    := rar-bridge rar-led

.PHONY: all test vet build arm64 dist install proto clean

all: vet test dist

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Host build (for running with -dry-run / tests on a workstation).
build:
	@mkdir -p $(BUILD)/host
	for c in $(CMDS); do $(GO) build -ldflags "$(LDFLAGS)" -o $(BUILD)/host/$$c ./cmd/$$c || exit 1; done

# Raspberry Pi 4 running OpenWrt (bcm2711, aarch64). Static, no cgo.
arm64:
	@mkdir -p $(BUILD)/arm64
	for c in $(CMDS); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD)/arm64/$$c ./cmd/$$c || exit 1; \
	done

dist: arm64
	rm -rf $(ROOTFS)
	mkdir -p $(ROOTFS)/usr/bin $(ROOTFS)/etc/init.d $(ROOTFS)/etc/config
	cp $(BUILD)/arm64/rar-bridge $(BUILD)/arm64/rar-led $(ROOTFS)/usr/bin/
	cp packaging/openwrt/files/etc/init.d/rar-bridge packaging/openwrt/files/etc/init.d/rar-led $(ROOTFS)/etc/init.d/
	chmod 0755 $(ROOTFS)/usr/bin/* $(ROOTFS)/etc/init.d/*
	# Shipped as rar.default so an install never overwrites edited settings.
	cp packaging/openwrt/files/etc/config/rar $(ROOTFS)/etc/config/rar.default
	# Files only, so extracting over / never changes existing directory modes.
	cd $(ROOTFS) && find . -type f | sort | tar --owner=0 --group=0 --no-recursion -T - -czf ../$(notdir $(TARBALL))
	@echo "built $(TARBALL)"

# OpenWrt's dropbear has no SFTP server, so stream the tarball over ssh
# instead of using scp.
install: dist
	@test -n "$(HOST)" || { echo "usage: make install HOST=root@<radio-ip>"; exit 1; }
	ssh $(HOST) 'tar -xzf - -C /' < $(TARBALL)
	ssh $(HOST) '[ -f /etc/config/rar ] || cp /etc/config/rar.default /etc/config/rar; \
		/etc/init.d/rar-led enable; /etc/init.d/rar-bridge enable; \
		/etc/init.d/rar-led restart; /etc/init.d/rar-bridge restart'

proto:
	./scripts/gen-proto.sh

clean:
	rm -rf $(BUILD)
