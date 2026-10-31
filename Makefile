BINARY  := rla
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: all build test lint app app-dev clean

all: test build

## build: build bin/rla
build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/$(BINARY)

## test: run tests with the race detector
test:
	go test -race ./...

## lint: run go vet
lint:
	go vet ./...

## app-dev: run the desktop app against bin/rla
app-dev: build
	cd app && npm install && npm run dev

## app: build the desktop app (app/dist)
app: build
	cd app && npm install && npm run build

clean:
	rm -rf bin/ app/out app/dist
