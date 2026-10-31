BINARY  := rla
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: all build test cover lint fmt typecheck check app app-dev clean

all: test build

## build: build bin/rla
build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/$(BINARY)

## test: run tests with the race detector
test:
	go test -race ./...

## cover: run tests and write coverage.out
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

## lint: go vet, then golangci-lint (.golangci.yml) if it is installed
lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; \
	else echo "golangci-lint not installed; ran go vet only"; fi

## fmt: format the Go code
fmt:
	gofmt -w cmd internal

## typecheck: type-check the desktop app
typecheck:
	cd app && npm run typecheck

## check: everything CI should run
check: lint test typecheck

## app-dev: run the desktop app against bin/rla
app-dev: build
	cd app && npm install && npm run dev

## app: build the desktop app (app/dist)
app: build
	cd app && npm install && npm run build

clean:
	rm -rf bin/ app/out app/dist coverage.out
