# corescope build entry point.
#
# This repo is 14 Go modules wired with `replace ../../internal/*` and no
# go.work, so every recipe changes into its module. The build flags live here
# because the SQLite driver is cgo now (github.com/mattn/go-sqlite3) and the
# flags stopped being something you can safely retype at each call site.
#
# Cross-compilation uses `zig cc` as the C toolchain, targeting musl so the
# binaries are fully static and run on the alpine runtime image (or scratch)
# with no libc dependency at all.
#
# Quick reference:
#   make build                    # all four binaries for the host
#   make build-server             # just one
#   make crossbuild               # linux/amd64 + linux/arm64, static, into dist/
#   make test / vet / fmt-check   # across all modules
#   make docker-build             # multi-arch image via buildx

GO                ?= $(shell command -v go)
GIT_VERSION       ?= $(shell git describe --tags --match "v*" 2>/dev/null || echo unknown)
GIT_COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GOENV_GOOS        := $(shell $(GO) env GOOS)
GOENV_GOARCH      := $(shell $(GO) env GOARCH)
GOOS              ?= $(GOENV_GOOS)
GOARCH            ?= $(GOENV_GOARCH)

# Exported: a bare make variable never reaches the go toolchain.
export CGO_ENABLED ?= 1

# netgo/osusergo keep the pure-Go resolver and user lookup the binaries had
# under CGO_ENABLED=0, so enabling cgo for SQLite does not quietly switch DNS
# resolution to the C resolver. sqlite_omit_load_extension drops the dlopen
# path, which is what lets -extldflags -static link cleanly. -Wl,-s on the
# cross builds strips the musl objects zig links in: Go's own -s -w does not
# reach them, and they account for well over half the binary.
GO_BUILD_TAGS     ?= netgo,osusergo,sqlite_omit_load_extension
GO_BUILD_FLAGS    ?= -trimpath
GO_LDFLAGS_OPTIMS ?= -s -w

CMDS              := server ingestor decrypt migrate
DIST              := dist

# Per-binary version stamping. Only these two read build metadata; ingestor and
# migrate take none. Kept as make variables so crossbuild and the host build
# cannot drift apart.
LDFLAGS_server    := -X main.Version=$(GIT_VERSION) -X main.Commit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)
LDFLAGS_ingestor  :=
LDFLAGS_decrypt   := -X main.version=$(GIT_VERSION)
LDFLAGS_migrate   :=

# zig target triples for the platforms we ship. musl, so the result is static.
ZIG_TARGET_linux_amd64 := x86_64-linux-musl
ZIG_TARGET_linux_arm64 := aarch64-linux-musl
CROSS_PLATFORMS   := linux/amd64 linux/arm64

DOCKER_IMAGE      ?= ghcr.io/kpa-clawbot/corescope
DOCKER_TAG        ?= $(GIT_VERSION)
DOCKER_PLATFORMS  ?= linux/amd64,linux/arm64

.PHONY: all build crossbuild test vet fmt-check tidy clean docker-build docker-push help

all: build

help:
	@echo "targets: build crossbuild test vet fmt-check tidy clean docker-build docker-push"
	@echo "         build-{$(shell echo $(CMDS) | tr ' ' ',')}"

# -- host builds ---------------------------------------------------------------

build: $(addprefix build-,$(CMDS))

build-%:
	@mkdir -p $(DIST)
	cd cmd/$* && GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build \
		-tags $(GO_BUILD_TAGS) $(GO_BUILD_FLAGS) \
		-ldflags "$(GO_LDFLAGS_OPTIMS) $(LDFLAGS_$*)" \
		-o ../../$(DIST)/corescope-$* .

# -- cross builds --------------------------------------------------------------
#
# The host build above deliberately leaves CC alone: native builds, `go vet` and
# `-race` must use the host compiler. Only these recipes hand the build to zig.

crossbuild: $(foreach p,$(CROSS_PLATFORMS),$(foreach c,$(CMDS),crossbuild-$(c)-$(subst /,-,$(p))))

define CROSSBUILD_RULE
.PHONY: crossbuild-$(2)-$(3)-$(4)
crossbuild-$(2)-$(3)-$(4):
	@mkdir -p $$(DIST)
	@command -v zig >/dev/null || { echo "zig not found: needed to cross-compile cgo. See https://ziglang.org/download/"; exit 1; }
	cd cmd/$(2) && CGO_ENABLED=1 GOOS=$(3) GOARCH=$(4) CC="zig cc -target $(1)" \
		$$(GO) build -tags $$(GO_BUILD_TAGS) $$(GO_BUILD_FLAGS) \
			-ldflags '$$(GO_LDFLAGS_OPTIMS) -extldflags "-static -Wl,-s" $$(LDFLAGS_$(2))' \
			-o ../../$$(DIST)/corescope-$(2)-$(3)-$(4) .
endef

$(foreach c,$(CMDS),\
  $(eval $(call CROSSBUILD_RULE,$(ZIG_TARGET_linux_amd64),$(c),linux,amd64))\
  $(eval $(call CROSSBUILD_RULE,$(ZIG_TARGET_linux_arm64),$(c),linux,arm64)))

# -- checks --------------------------------------------------------------------

test:
	bash scripts/allmod.sh test ./...

vet:
	bash scripts/allmod.sh vet ./...

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go' | grep -vi 'Dockerfile.go')); \
	if [ -n "$$unformatted" ]; then echo "gofmt required on:"; echo "$$unformatted"; exit 1; fi; \
	echo "gofmt: clean"

tidy:
	bash scripts/allmod.sh mod tidy

clean:
	rm -rf $(DIST)

# -- docker --------------------------------------------------------------------

docker-build:
	docker buildx build . \
		--platform=$(DOCKER_PLATFORMS) \
		--build-arg=APP_VERSION=$(GIT_VERSION) \
		--build-arg=GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg=BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-push: DOCKER_PUSH_FLAG := --push
docker-push:
	docker buildx build . \
		--platform=$(DOCKER_PLATFORMS) \
		--build-arg=APP_VERSION=$(GIT_VERSION) \
		--build-arg=GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg=BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) $(DOCKER_PUSH_FLAG)
