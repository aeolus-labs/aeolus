.PHONY: all build build-go build-web dev-web test clean release-test release-snapshot

all: build

# Build the React dashboard then the Go binary with embedded assets.
build: build-web build-go

build-web:
	cd dashboard && npm install --no-fund --no-audit --silent && npm run build

build-go:
	go build -o aeolus ./cmd/aeolus

# Run the Vite dev server. In another terminal, run aeolus normally —
# the Vite server proxies /api/* to it.
dev-web:
	cd dashboard && npm run dev

test:
	go test ./...

# Verify goreleaser config without producing artifacts.
release-test:
	goreleaser check

# Build cross-platform release artifacts locally (dist/), no publish.
# Useful for sanity-checking the pipeline before tagging.
release-snapshot:
	goreleaser release --snapshot --clean --skip=publish

clean:
	rm -f aeolus
	rm -rf internal/dashboard/web/index.html internal/dashboard/web/assets
	rm -rf dashboard/node_modules
	rm -rf dist
