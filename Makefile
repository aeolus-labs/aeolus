.PHONY: all build build-go build-web dev-web install test clean release-test release-snapshot

all: build

# Build the React dashboard then the Go binary with embedded assets.
build: build-web build-go

build-web:
	cd dashboard && npm install --no-fund --no-audit --silent && npm run build

build-go:
	go build -o aeolus ./cmd/aeolus

# Local dev loop: build, replace the binary at /usr/local/bin (where
# launchd loads it from per the plist), then restart the service so
# changes take effect. One command instead of three.
install: build
	sudo install -m 0755 ./aeolus /usr/local/bin/aeolus
	aeolus service restart
	@echo
	@echo "Hard-refresh the dashboard (Cmd+Shift+R) so the browser picks up the new JS/CSS."

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
