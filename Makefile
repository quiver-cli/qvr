BINARY := qvr
BUILD_DIR := bin
MODULE := github.com/raks097/quiver
VERSION ?= 0.4.9
LDFLAGS := -ldflags "-X $(MODULE)/cmd.version=$(VERSION)"

INSTALL_DIR ?= /usr/local/bin

.PHONY: all build install test lint fmt clean

all: fmt lint test build

build:
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) .

install: build
	@if [ -w "$(INSTALL_DIR)" ]; then \
		cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY); \
	else \
		sudo cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY); \
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
