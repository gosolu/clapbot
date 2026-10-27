# ==============================================================================
# Clapbot Makefile
# Pure Go AI Assistant & Multi-Channel Agent Gateway (CGO_ENABLED=0)
# ==============================================================================

BINARY_NAME ?= clapbot
MAIN_PKG    ?= ./cmd/clapbot
DIST_DIR    ?= dist
PKGS        ?= ./...

# Pure Go build constraints
CGO_ENABLED ?= 0
GO          ?= go

# Build flags
BUILD_FLAGS ?= -trimpath
LDFLAGS     ?= -s -w

GO_ENV      := CGO_ENABLED=$(CGO_ENABLED)

.PHONY: all help build install clean test test-short test-cover fmt fmt-check vet lint tidy verify run chat serve onboard build-linux build-darwin build-windows build-all

# Default target
all: build

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ": "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## build: Build the pure-Go clapbot binary (CGO_ENABLED=0)
build:
	$(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(MAIN_PKG)

## install: Install binary to GOBIN
install:
	$(GO_ENV) $(GO) install $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" $(MAIN_PKG)

## clean: Remove built binaries, coverage artifacts, and dist files
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME).exe coverage.out coverage.html
	rm -rf $(DIST_DIR)

## test: Run all unit and integration tests
test:
	$(GO_ENV) $(GO) test -v $(PKGS)

## test-short: Run fast unit tests (skipping long integration tests)
test-short:
	$(GO_ENV) $(GO) test -v -short $(PKGS)

## test-cover: Run tests with code coverage report
test-cover:
	$(GO_ENV) $(GO) test -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out
	@echo "HTML coverage report: $(GO) tool cover -html=coverage.out -o coverage.html"

## fmt: Format all Go source files
fmt:
	$(GO) fmt $(PKGS)

## fmt-check: Check if all Go files are properly formatted
fmt-check:
	@diff=$$(gofmt -l .); \
	if [ -n "$$diff" ]; then \
		echo "The following files are not formatted:"; \
		echo "$$diff"; \
		exit 1; \
	fi

## vet: Run go vet on all packages
vet:
	$(GO) vet $(PKGS)

## lint: Run static analysis / vetting
lint: vet

## tidy: Run go mod tidy to clean up dependencies
tidy:
	$(GO) mod tidy

## verify: Run formatting check, vetting, and all tests
verify: fmt-check vet test

## run: Run a single prompt (e.g. make run PROMPT="Hello!")
run: build
	./$(BINARY_NAME) run -m "$(if $(PROMPT),$(PROMPT),Hello from clapbot!)"

## chat: Start interactive terminal REPL
chat: build
	./$(BINARY_NAME) chat

## serve: Start the local Gateway daemon, Scheduler, and chat channels
serve: build
	./$(BINARY_NAME) serve

## onboard: Run the interactive onboarding wizard
onboard: build
	./$(BINARY_NAME) onboard

## build-linux: Cross-compile 64-bit Linux binaries (amd64 and arm64) into dist/
build-linux:
	@mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PKG)
	GOOS=linux GOARCH=arm64 $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_PKG)

## build-darwin: Cross-compile macOS binaries (amd64 and arm64) into dist/
build-darwin:
	@mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=amd64 $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PKG)
	GOOS=darwin GOARCH=arm64 $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PKG)

## build-windows: Cross-compile Windows 64-bit binary into dist/
build-windows:
	@mkdir -p $(DIST_DIR)
	GOOS=windows GOARCH=amd64 $(GO_ENV) $(GO) build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PKG)

## build-all: Cross-compile binaries for Linux, macOS, and Windows into dist/
build-all: build-linux build-darwin build-windows
