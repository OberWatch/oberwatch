.PHONY: build run test lint clean fmt vet dashboard

BINARY_NAME := oberwatch
BUILD_DIR := bin
CMD_DIR := ./cmd/oberwatch

# Build the oberwatch binary
build:
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

# Run the oberwatch binary
run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run all tests
test:
	go test ./...

# Run tests with race detector
test-race:
	go test -race ./...

# Run linter
lint:
	golangci-lint run

# Format code
fmt:
	gofmt -w .

# Run go vet
vet:
	go vet ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)

# Build the SvelteKit dashboard
dashboard:
	cd dashboard/svelte && npm run build
