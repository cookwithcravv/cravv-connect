.PHONY: all build test vet relay-test clean

GO ?= go

all: vet test build

build:
	$(GO) build -o bin/cravv-connect ./cmd/cravv-connect
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
