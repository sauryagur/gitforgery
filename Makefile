# gitforgery build & test automation.
SHELL := /bin/bash

BINARY     := gitforgery
MODULE     := $(shell go list -m)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS    := -trimpath
LDFLAGS    := -s -w -X '$(MODULE)/cmd.version=$(VERSION)'

.PHONY: all build install test test-race vet lint fmt fmt-fix tidy cover \
	release snapshot clean help

.DEFAULT_GOAL := all

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

install:
	go install -ldflags '$(LDFLAGS)' $(MODULE)

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.txt ./...
	go tool cover -func=coverage.txt

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }

fmt-fix:
	gofmt -w .

tidy:
	go mod tidy

release:
	goreleaser release --clean

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin coverage.txt

help:
	@echo "Targets:"
	@echo "  build       compile bin/$(BINARY) (default)"
	@echo "  install      go install to GOBIN"
	@echo "  test         run tests"
	@echo "  test-race   run tests with race detector"
	@echo "  cover         run tests and print coverage"
	@echo "  vet          run go vet"
	@echo "  lint         run golangci-lint"
	@echo "  fmt           check gofmt; fmt-fix applies it"
	@echo "  tidy         run go mod tidy"
	@echo "  release      build a release via goreleaser"
	@echo "  snapshot     build unfinalized binaries via goreleaser"
	@echo "  clean        remove build artifacts"