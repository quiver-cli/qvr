BINARY    := qvr
BUILD_DIR := bin
MODULE    := github.com/raks097/quiver

# --- Version / provenance -------------------------------------------------
# The version NUMBER is hardcoded in ONE place — the VERSION file — and you
# bump it deliberately. `make release-check` enforces that a release tag matches
# it, so a tagged build can never ship a number you didn't declare. The commit
# SHA (with a -dirty suffix for uncommitted trees) and build date ride along as
# SEPARATE provenance, so a dev build is still unmistakable from a clean release
# without polluting the version number itself.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
ifneq ($(shell git status --porcelain 2>/dev/null),)
  COMMIT := $(COMMIT)-dirty
endif

LDFLAGS := -ldflags "-s -w \
	-X $(MODULE)/cmd.version=$(VERSION) \
	-X $(MODULE)/cmd.commit=$(COMMIT) \
	-X $(MODULE)/cmd.date=$(DATE)"

INSTALL_DIR ?= /usr/local/bin

# --- Source sets drive incremental rebuilds -------------------------------
# Touch nothing → make does nothing. Touch UI source → only the UI rebuilds.
# Touch Go source → only the binary relinks. No stale artifact survives an edit.
GO_SRC := $(shell find . -name '*.go' -not -path './ui/*' 2>/dev/null) go.mod go.sum
UI_SRC := $(shell find ui -type f -not -path 'ui/node_modules/*' -not -path 'ui/dist/*' 2>/dev/null)

BIN  := $(BUILD_DIR)/$(BINARY)
DIST := internal/ui/dist/index.html

.PHONY: all build build-go ui ui-dev install verify release-check test lint fmt clean run

all: fmt lint test build

# build → THE command. Ensures the real dashboard is current (rebuilt only when
# ui/ changed), then compiles bin/qvr with that UI embedded and the git version
# stamped in. Re-running with no source change is a no-op.
build: $(BIN)

# VERSION is a prerequisite so a deliberate version bump always relinks — the
# stamped number must never lag the file.
$(BIN): $(GO_SRC) $(DIST) VERSION
	go build $(LDFLAGS) -o $(BIN) .
	@echo "built $(BIN) — $(VERSION) ($(COMMIT))"

# The embedded dashboard. Regenerated only when UI sources change; `npm ci`
# pins to the lockfile for reproducibility. Vite's emptyOutDir removes the
# committed .gitkeep, so restore it — the //go:embed all:dist glob needs at
# least one tracked file to compile a Node-less (build-go) build.
$(DIST): $(UI_SRC)
	cd ui && npm ci && npm run build
	@touch internal/ui/dist/.gitkeep

ui: $(DIST)

# build-go → escape hatch: compile WITHOUT Node. For CI and goreleaser, which
# build the dashboard separately (goreleaser before-hook). Embeds whatever
# dist/ is present — only safe when the UI was just built or is intentionally
# the stub. Prefer `make build` for local work.
build-go: $(GO_SRC) VERSION
	go build $(LDFLAGS) -o $(BIN) .
	@echo "built $(BIN) (go-only) — $(VERSION) ($(COMMIT))"

ui-dev:
	cd ui && npm run dev

# install → build the real thing (fresh UI + git version), then do an ATOMIC,
# new-inode replace: copy to a temp file in the target dir, then rename over the
# final path. Overwriting a binary in place (plain cp) while an instance is
# running poisons its code-signing vnode on macOS/Apple Silicon — the kernel
# then SIGKILLs every later exec ("Killed: 9"). A rename gives the new binary a
# fresh inode and leaves any running process on its old, still-valid one.
# Ends with `verify` so a stale copy elsewhere on PATH can't hide.
install: build
	@src="$(BIN)"; \
	dst="$(INSTALL_DIR)/$(BINARY)"; \
	tmp="$(INSTALL_DIR)/.$(BINARY).tmp.$$$$"; \
	if [ -w "$(INSTALL_DIR)" ]; then \
		cp "$$src" "$$tmp" && mv -f "$$tmp" "$$dst" || { rm -f "$$tmp"; exit 1; }; \
	else \
		sudo cp "$$src" "$$tmp" && sudo mv -f "$$tmp" "$$dst" || { sudo rm -f "$$tmp"; exit 1; }; \
	fi
	@echo "installed $(VERSION) → $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) -s verify

# verify → zero-staleness check: the binary you just built vs the qvr actually
# resolved on $PATH. If they disagree, a stale install is shadowing the new one.
verify:
	@built="$$($(BIN) version 2>/dev/null | head -n1 | awk '{print $$2}')"; \
	onpath_bin="$$(command -v $(BINARY) 2>/dev/null || true)"; \
	onpath="$$($(BINARY) version 2>/dev/null | head -n1 | awk '{print $$2}' || echo none)"; \
	printf 'repo build : %s -> %s\n' "$(BIN)" "$${built:-none}"; \
	printf 'on PATH    : %s -> %s\n' "$${onpath_bin:-<not found>}" "$${onpath}"; \
	if [ -n "$$onpath_bin" ] && [ "$$built" != "$$onpath" ]; then \
		printf '\033[1;33mWARN\033[0m  PATH qvr (%s) != repo build (%s) — run: make install\n' "$$onpath" "$$built"; \
	else \
		printf '\033[1;32mOK\033[0m    in sync\n'; \
	fi

# release-check → guard the single source: HEAD must carry an exact semver tag
# (vX.Y.Z) whose number equals the VERSION file. Run this before publishing so a
# release can never disagree with the declared version. CI runs it too.
release-check:
	@fv="$$(cat VERSION 2>/dev/null)"; \
	tv="$$(git describe --tags --exact-match 2>/dev/null | sed 's/^v//')"; \
	if [ -z "$$fv" ]; then echo "no VERSION file"; exit 1; fi; \
	if [ -z "$$tv" ]; then echo "HEAD has no exact vX.Y.Z tag — tag v$$fv before releasing"; exit 1; fi; \
	if [ "$$fv" != "$$tv" ]; then echo "VERSION ($$fv) != tag (v$$tv) — bump one to match"; exit 1; fi; \
	echo "release OK: VERSION and tag agree on $$fv"

test:
	go test ./... -v -count=1

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

clean:
	rm -rf $(BUILD_DIR)

# run → mirrors a real build's version stamping so `make run ARGS=version`
# reports the same provenance a built binary would, not a bare "dev".
run:
	go run $(LDFLAGS) . $(ARGS)
