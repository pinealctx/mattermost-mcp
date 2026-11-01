BINARY_NAME := mattermost-mcp
GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: build test lint clean release
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o build/$(BINARY_NAME) .
test:
	$(GO) test -race ./...
lint:
	$(GO) vet ./...
	golangci-lint run ./...
clean:
	rm -rf build dist
release:
	GO='$(GO)' VERSION='$(VERSION)' sh scripts/release.sh
