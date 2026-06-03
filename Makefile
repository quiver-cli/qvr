BINARY := qvr
BUILD_DIR := bin
MODULE := github.com/raks097/quiver
VERSION ?= 0.10.3
LDFLAGS := -ldflags "-X $(MODULE)/cmd.version=$(VERSION)"

INSTALL_DIR ?= /usr/local/bin

.PHONY: all build build-all install test lint fmt clean ui ui-dev

all: fmt lint test build

build:
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) .

# build-all bakes the React dashboard into the binary. Use for releases.
# Plain `make build` stays Go-only (no Node) and embeds whatever dist/ is
# present — the committed stub if the UI was never built.
build-all: ui build

# ui builds the React dashboard into internal/ui/dist (embedded via go:embed).
# Vite's emptyOutDir removes the committed .gitkeep; restore it so the embed
# pattern keeps matching for the next Go-only build.
ui:
	cd ui && npm ci && npm run build
	@touch internal/ui/dist/.gitkeep

# ui-dev runs the Vite dev server (proxies /api to a local `qvr ui`).
ui-dev:
	cd ui && npm run dev

# install does an ATOMIC, new-inode replace: copy to a temp file in the target
# dir, then `mv` (rename) it over the final path. Overwriting the binary in
# place (plain `cp`) while an instance is still running poisons its code-signing
# vnode on macOS/Apple Silicon — the kernel then SIGKILLs every later exec of
# that path ("Killed: 9"). A rename gives the new binary a fresh inode and
# leaves any running process on its old (still-valid) one.
install: build
	@src="$(BUILD_DIR)/$(BINARY)"; \
	dst="$(INSTALL_DIR)/$(BINARY)"; \
	tmp="$(INSTALL_DIR)/.$(BINARY).tmp.$$$$"; \
	if [ -w "$(INSTALL_DIR)" ]; then \
		cp "$$src" "$$tmp" && mv -f "$$tmp" "$$dst" || { rm -f "$$tmp"; exit 1; }; \
	else \
		sudo cp "$$src" "$$tmp" && sudo mv -f "$$tmp" "$$dst" || { sudo rm -f "$$tmp"; exit 1; }; \
	fi
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"

test:
	go test ./... -v -count=1

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

clean:
	rm -rf $(BUILD_DIR)

run:
	go run . $(ARGS)
