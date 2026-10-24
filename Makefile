# tinwire: build, test and install.
#
#   make                 build ./tinwire for this machine
#   make test            go vet + race-enabled unit tests
#   make install         install to ~/.local/bin (PREFIX=... to change)
#   make dist            release archives for linux/amd64, linux/arm64, darwin/arm64 in dist/
#   make smoke           drive the real TUI in a pseudo-terminal (dry-run; needs a configured host)
#   make install-remote REMOTE=user@linuxbox   cross-build and install on another Linux machine

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local
REMOTE  ?=
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64

.PHONY: build test install dist smoke install-remote clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o tinwire .

test:
	go vet ./...
	go test -race -count=1 ./...

install: build
	install -d $(PREFIX)/bin
	install -m 755 tinwire $(PREFIX)/bin/.tinwire.new
	mv -f $(PREFIX)/bin/.tinwire.new $(PREFIX)/bin/tinwire

dist:
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=tinwire-$$os-$$arch; \
		echo "building $$name"; \
		mkdir -p dist/$$name && \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/tinwire . && \
		cp README.md LICENSE dist/$$name/ && \
		tar -C dist -czf dist/$$name.tar.gz $$name && rm -rf dist/$$name; \
	done
	cd dist && (command -v sha256sum >/dev/null && sha256sum *.tar.gz || shasum -a 256 *.tar.gz) > SHA256SUMS

smoke:
	python3 scripts/pty-smoke.py ./tinwire

# Upload under a temporary name, then rename: works while tinwire is running there.
install-remote:
	@test -n "$(REMOTE)" || { echo "usage: make install-remote REMOTE=user@host"; exit 2; }
	CGO_ENABLED=0 GOOS=linux GOARCH=$${ARCH:-amd64} go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tinwire-remote .
	ssh $(REMOTE) 'mkdir -p ~/.local/bin'
	scp -q dist/tinwire-remote $(REMOTE):.local/bin/.tinwire.new
	ssh $(REMOTE) 'chmod 755 ~/.local/bin/.tinwire.new && mv -f ~/.local/bin/.tinwire.new ~/.local/bin/tinwire && tinwire --version'

clean:
	rm -rf dist tinwire
