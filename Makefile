.PHONY: build test vet demo run cross release clean

# Optional release version, e.g. make cross VERSION=v0.2.0. Without it the
# binary reports the module version Go derives from the Git tag or commit.
VERSION ?=
LDFLAGS := $(if $(VERSION),-ldflags "-X main.version=$(VERSION)")

build:
	go build -trimpath $(LDFLAGS) -o bin/lhwatch .

test:
	go test -race ./...
	python3 -m unittest discover -s internal/collector -p 'test_*.py' -v
	python3 -m unittest discover -s scripts -p 'test_*.py' -v

vet:
	go vet ./...

run: build
	./bin/lhwatch

demo: build
	./bin/lhwatch --demo

cross:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath $(LDFLAGS) -o dist/lhwatch-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath $(LDFLAGS) -o dist/lhwatch-darwin-arm64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath $(LDFLAGS) -o dist/lhwatch-linux-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath $(LDFLAGS) -o dist/lhwatch-darwin-amd64 .

PLATFORMS := linux/arm64 linux/amd64 darwin/arm64 darwin/amd64

# Release archives and checksums in dist/release: make release VERSION=v0.1.0
# Each archive holds lhwatch, the README, and the example configuration.
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z" >&2; exit 2; }
	rm -rf dist/release && mkdir -p dist/release
	set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; name=lhwatch_$(VERSION)_$${os}_$${arch}; \
		mkdir -p dist/release/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath $(LDFLAGS) -o dist/release/$$name/lhwatch .; \
		cp README.md lhwatch.example.json dist/release/$$name/; \
		tar --no-xattrs -czf dist/release/$$name.tar.gz -C dist/release $$name; \
		rm -rf dist/release/$$name; \
	done
	cd dist/release && { if command -v sha256sum >/dev/null; then sha256sum *.tar.gz; else shasum -a 256 *.tar.gz; fi; } > SHA256SUMS

clean:
	rm -rf bin dist
