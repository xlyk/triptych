GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt test lint

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

lint:
	$(GOLANGCI_LINT) run ./...
