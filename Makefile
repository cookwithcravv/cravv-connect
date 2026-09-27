.PHONY: all build test vet relay-test clean

GO ?= go

# github.com/msteinert/pam compiles with -std=c99, which hides RTLD_NEXT on
# older glibc headers (Ubuntu 22.04) unless _GNU_SOURCE is defined.
ifeq ($(shell uname -s),Linux)
export CGO_CFLAGS ?= -O2 -g -D_GNU_SOURCE
endif
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

all: vet test build

build:
	$(GO) build -ldflags "-X github.com/cookwithcravv/cravv-connect/internal/cli.Version=$(VERSION)" -o bin/cravv-connect ./cmd/cravv-connect
	CGO_ENABLED=0 $(GO) build -o bin/cravv-relay ./cmd/cravv-relay
	CGO_ENABLED=0 $(GO) build -o bin/cravv-conformance ./cmd/cravv-conformance

test:
	$(GO) test ./... -race -count=1

vet:
	$(GO) vet ./...

relay-test:
	cd relay-cf && npm test

clean:
	rm -rf bin

.PHONY: relay-cf-test conformance-cf

relay-cf-test:
	cd relay-cf && npm ci && npm run typecheck && npm test

conformance-cf:
	cd relay-cf && npm ci
	relay-cf/scripts/conformance.sh
