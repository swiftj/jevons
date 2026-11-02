VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
BINARIES := jev-router jev-runner jev-hook
PREFIX ?= $(HOME)/.local
AGENT_LABEL := com.github.swiftj.jev-router
AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(AGENT_LABEL).plist

.PHONY: build test cover bench lint vet check release run-router clean install uninstall install-agent uninstall-agent

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

install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(addprefix bin/,$(BINARIES)) $(PREFIX)/bin/

uninstall: uninstall-agent
	rm -f $(addprefix $(PREFIX)/bin/,$(BINARIES))

# install-agent runs jev-router at login via launchd (macOS only).
install-agent: install
	install -d -m 0700 $(HOME)/.jev
	install -d $(dir $(AGENT_PLIST))
	sed -e 's|@BIN@|$(PREFIX)/bin|g' -e 's|@HOME@|$(HOME)|g' -e 's|@LABEL@|$(AGENT_LABEL)|g' \
		deploy/launchd/jev-router.plist > $(AGENT_PLIST)
	-launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null
	launchctl bootstrap gui/$$(id -u) $(AGENT_PLIST)

uninstall-agent:
	-launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null
	rm -f $(AGENT_PLIST)

run-router:
	go run ./cmd/jev-router

clean:
	rm -rf bin dist coverage.out
