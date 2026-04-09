GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt test lint e2e

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

lint:
	$(GOLANGCI_LINT) run ./...

e2e:
	python3 scripts/e2e_smoke.py
