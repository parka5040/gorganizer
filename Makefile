GO          := go
GOFLAGS     := -trimpath
BINARY      := gorganizerd
CMD         := ./cmd/gorganizerd
CTL_BINARY  := gorganizerctl
CTL_CMD     := ./cmd/gorganizerctl
PROTO_DIR   := api/proto
PROTO_SRC   := $(PROTO_DIR)/gorganizer.proto
PROTO_GO    := $(PROTO_DIR)/gorganizer.pb.go
PROTO_GRPC  := $(PROTO_DIR)/gorganizer_grpc.pb.go
TOOLS_BIN   := $(CURDIR)/.tools/bin
OUT_DIR     ?= .
GUI_BUILD_DIR ?= build

# Version stamping. The VERSION file at the repo root is the canonical
# source of truth — bump it on release. `git describe` is appended as a
# build-time decoration when the working tree differs from the tagged
# version (so dev builds carry the commit short-sha + dirty flag).
# COMMIT/DATE follow the same pattern. CI may override VERSION explicitly
# (e.g. for tagged releases) by passing VERSION=... on the command line.
VERSION_FILE_VALUE := $(shell sed -n '1{s/[[:space:]]*$$//;p;}' VERSION 2>/dev/null)
GIT_DESC          := $(shell git describe --tags --always --dirty 2>/dev/null)
ifeq ($(strip $(VERSION_FILE_VALUE)),)
VERSION ?= $(if $(GIT_DESC),$(GIT_DESC),dev)
else
ifeq ($(strip $(GIT_DESC)),)
VERSION ?= $(VERSION_FILE_VALUE)
else
VERSION ?= $(VERSION_FILE_VALUE)+$(GIT_DESC)
endif
endif
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: all build ctl test vet clean proto gui install check-comments verify

all: proto build ctl

proto: $(PROTO_GO) $(PROTO_GRPC)

$(PROTO_GO) $(PROTO_GRPC): $(PROTO_SRC)
	# Build pinned protobuf plugins locally so fresh clones need no global Go tools.
	mkdir -p $(TOOLS_BIN)
	$(GO) build -o $(TOOLS_BIN)/ \
		google.golang.org/protobuf/cmd/protoc-gen-go \
		google.golang.org/grpc/cmd/protoc-gen-go-grpc
	PATH="$(TOOLS_BIN):$(HOME)/go/bin:$(PATH)" protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_SRC)

build: proto
	mkdir -p "$(OUT_DIR)"
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o "$(OUT_DIR)/$(BINARY)" $(CMD)

ctl: proto
	mkdir -p "$(OUT_DIR)"
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o "$(OUT_DIR)/$(CTL_BINARY)" $(CTL_CMD)

gui:
	cmake -B "$(GUI_BUILD_DIR)" -DCMAKE_BUILD_TYPE=Release -DGORGANIZER_VERSION="$(VERSION)"
	cmake --build "$(GUI_BUILD_DIR)" -j$$(command -v nproc >/dev/null 2>&1 && nproc || echo 1)

test:
	$(GO) test -race -count=1 ./internal/... ./cmd/... ./scripts/...

vet:
	$(GO) vet ./...

check-comments:
	$(GO) run ./scripts/commentcheck

verify: vet test check-comments

clean:
	rm -f "$(OUT_DIR)/$(BINARY)" "$(OUT_DIR)/$(CTL_BINARY)"
	rm -f $(PROTO_GO) $(PROTO_GRPC)
	rm -rf "$(GUI_BUILD_DIR)" .tools

install: build
	install -Dm755 "$(OUT_DIR)/$(BINARY)" $(DESTDIR)/usr/bin/$(BINARY)
