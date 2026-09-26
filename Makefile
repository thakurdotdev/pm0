BINARY := pm0
FAKECHILD := fakechild
GO := go
PREFIX ?= /usr/local
# Release version priority:
# 1. VERSION environment/CLI override (e.g. `make release VERSION=0.1.3`)
# 2. Exact git tag at HEAD (e.g. `v0.1.3` -> `0.1.3`)
# 3. package.json version
# 4. git describe fallback
# 5. Default 1.0.0
GIT_EXACT_TAG := $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//')
PKG_VERSION := $(shell node -p "require('./package.json').version" 2>/dev/null || echo "")
GIT_DESCRIBE := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')

VERSION ?= $(if $(GIT_EXACT_TAG),$(GIT_EXACT_TAG),$(if $(PKG_VERSION),$(PKG_VERSION),$(if $(GIT_DESCRIBE),$(GIT_DESCRIBE),1.0.0)))
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIST := dist

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
# pm0 is Linux-only by design (cgroup v2 kill paths, /proc introspection).
PLATFORMS := linux/amd64 linux/arm64 linux/arm linux/386

.PHONY: all build fakechild test race vet fmt lint proto tidy clean install release npm-prebuilds npm-pack

all: build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/pm0

fakechild:
	$(GO) build -o bin/$(FAKECHILD) ./test/fakechild

# install: copy the binary into PREFIX/bin (builds first if bin/$(BINARY) does not exist)
install:
	@if [ ! -f bin/$(BINARY) ]; then $(MAKE) build; fi
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 bin/$(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	@echo "installed $(DESTDIR)$(PREFIX)/bin/$(BINARY) ($(VERSION) $(COMMIT))"

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)

# release: cross-compile static binaries + archives into dist/
release:
	rm -rf $(DIST) && mkdir -p $(DIST)
	@for plat in $(PLATFORMS); do \
          os=$${plat%/*}; arch=$${plat#*/}; \
          echo "== $${os}/$${arch}"; \
          CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} GOARM=$$([ "$${arch}" = arm ] && echo 7) \
            $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/pm0-$${os}-$${arch} ./cmd/pm0 || exit 1; \
          tar -C $(DIST) -czf $(DIST)/pm0-$${os}-$${arch}.tar.gz pm0-$${os}-$${arch}; \
	done
	@cd $(DIST) && sha256sum * > checksums.txt 2>/dev/null || true
	@echo "release archives in $(DIST)/ (version $(VERSION))"

# npm-prebuilds: stage a prebuild for THIS platform where the npm shim looks
npm-prebuilds: build
	@os=$$($(GO) env GOOS); arch=$$($(GO) env GOARCH); \
	mkdir -p prebuilds/$${os}-$${arch} && \
	cp bin/$(BINARY) prebuilds/$${os}-$${arch}/$(BINARY) && \
	echo "staged prebuilds/$${os}-$${arch}/"

# npm-pack: stage the prebuild and produce dist/pm0-<version>.tgz
npm-pack: npm-prebuilds
	mkdir -p $(DIST)
	npm pack --pack-destination $(DIST)
	@echo "npm tarball: $(DIST)/pm0-$(VERSION).tgz"

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint: vet
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed; skipping"

# Regenerate protobuf stubs. Requires buf + plugins on PATH:
#   go install github.com/bufbuild/buf/cmd/buf@v1.50.0
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
proto:
	buf generate api/v1

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin $(DIST)
