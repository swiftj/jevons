VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
PREFIX ?= $(HOME)/.local
SHASUM := $(shell command -v shasum >/dev/null 2>&1 && echo "shasum -a 256" || echo sha256sum)

.PHONY: build test cover bench lint vet check release run-router clean install uninstall

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/jev ./cmd/jev

test:
	go test -race -count=1 ./...

cover:
	go test -race -count=1 -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

bench:
	go test -run='^$$' -bench=. -benchmem ./internal/prefilter/... ./internal/router/...

lint:
	golangci-lint run

vet:
	go vet ./...

check: vet lint test

# release cross-compiles dist/jev_<os>_<arch>. Where lipo exists (macOS), the two
# darwin builds become one dist/jev_darwin_universal.
release:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "build jev $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/jev_$${os}_$${arch} ./cmd/jev || exit 1; \
	done
	@if command -v lipo >/dev/null 2>&1; then \
		echo "lipo jev_darwin_universal"; \
		lipo -create -output dist/jev_darwin_universal dist/jev_darwin_arm64 dist/jev_darwin_amd64 && \
		rm dist/jev_darwin_arm64 dist/jev_darwin_amd64; \
	fi
	@cd dist && $(SHASUM) jev_* > SHA256SUMS

# install runs `jev install`: binary, keychain key, launchd agent, Claude Code wiring.
install: build
	./bin/jev install -bin-dir $(PREFIX)/bin

uninstall:
	./bin/jev uninstall -bin-dir $(PREFIX)/bin

run-router:
	go run ./cmd/jev router

clean:
	rm -rf bin dist coverage.out
