VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
BINARIES := jev-router jev-runner

.PHONY: build test cover bench lint vet check release run-router clean

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/ ./cmd/...

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

release:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		for b in $(BINARIES); do \
			echo "build $$b $$os/$$arch"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
				-o dist/$${b}_$${os}_$${arch} ./cmd/$$b || exit 1; \
		done; \
	done
	@cd dist && shasum -a 256 jev-* > SHA256SUMS

run-router:
	go run ./cmd/jev-router

clean:
	rm -rf bin dist coverage.out
