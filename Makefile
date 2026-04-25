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

# Version stamping. CI sets VERSION on tag push; locally we fall back to
# `git describe`, then "dev" if no tags exist yet. COMMIT/DATE follow the
# same pattern.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: all build ctl test vet clean proto package gui

all: proto build ctl

proto: $(PROTO_GO) $(PROTO_GRPC)

$(PROTO_GO) $(PROTO_GRPC): $(PROTO_SRC)
	PATH="$(HOME)/go/bin:$(PATH)" protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_SRC)

build: proto
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY) $(CMD)

ctl: proto
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(CTL_BINARY) $(CTL_CMD)

gui:
	cmake -B build -DCMAKE_BUILD_TYPE=Release -DGORGANIZER_VERSION="$(VERSION)"
	cmake --build build -j$$(nproc)

test:
	$(GO) test -race -count=1 ./internal/...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY) $(CTL_BINARY)
	rm -f $(PROTO_GO) $(PROTO_GRPC)
	rm -rf build stage release gorganizer-v*-linux-*.tar.gz*

install: build
	install -Dm755 $(BINARY) $(DESTDIR)/usr/bin/$(BINARY)

# `make package` produces a release tarball locally. Mirrors what CI does in
# .github/workflows/release.yml, so contributors can smoke-test the install
# flow without pushing a tag.
package: all gui
	@root="gorganizer-$(VERSION)-linux-x86_64"; \
	rm -rf "release/$$root"; \
	mkdir -p "release/$$root/bin" \
	         "release/$$root/libexec" \
	         "release/$$root/share/applications" \
	         "release/$$root/share/icons/hicolor/256x256/apps"; \
	cp $(BINARY)          "release/$$root/bin/"; \
	cp $(CTL_BINARY)      "release/$$root/bin/"; \
	cp build/src/gorganizer "release/$$root/bin/gorganizer-gui"; \
	cp scripts/gorganizer-launcher.in "release/$$root/libexec/gorganizer-launcher"; \
	cp dist/gorganizer.desktop.in     "release/$$root/share/applications/"; \
	cp dist/gorganizer-nxm.desktop.in "release/$$root/share/applications/"; \
	[ -f resources/icons/gorganizer.png ] && \
	  cp resources/icons/gorganizer.png \
	     "release/$$root/share/icons/hicolor/256x256/apps/gorganizer.png" || true; \
	cp install.sh   "release/$$root/install.sh"; \
	cp uninstall.sh "release/$$root/uninstall.sh"; \
	[ -f LICENSE ]   && cp LICENSE   "release/$$root/" || true; \
	[ -f README.md ] && cp README.md "release/$$root/" || true; \
	echo "$(VERSION)" > "release/$$root/VERSION"; \
	chmod +x "release/$$root/bin/"* \
	         "release/$$root/libexec/gorganizer-launcher" \
	         "release/$$root/install.sh" \
	         "release/$$root/uninstall.sh"; \
	( cd release && tar -czf "../$$root.tar.gz" "$$root" ); \
	sha256sum "$$root.tar.gz" > "$$root.tar.gz.sha256"; \
	ls -lh "$$root.tar.gz" "$$root.tar.gz.sha256"
