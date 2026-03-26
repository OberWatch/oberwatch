.PHONY: tools build run test test-race lint clean fmt vet dashboard

BINARY_NAME := oberwatch
BUILD_DIR := bin
CMD_DIR := ./cmd/oberwatch
TOOLS_BIN := $(CURDIR)/.tools/bin
GO ?= go
GOFMT ?= gofmt
GOCACHE ?= $(CURDIR)/.tools/cache/go-build

export PATH := $(TOOLS_BIN):$(PATH)
export GOCACHE

tools:
	./scripts/dev/install-tools.sh

# Build the oberwatch binary
build:
	$(GO) build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

# Run the oberwatch binary
run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run all tests
test:
	$(GO) test ./...

# Run tests with race detector
test-race:
	$(GO) test -race ./...

# Run linter
lint: tools
	golangci-lint run

# Format code
fmt:
	$(GOFMT) -w .

# Run go vet
vet:
	$(GO) vet ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)

# Build the SvelteKit dashboard
dashboard:
	cd dashboard/svelte && npm run build
