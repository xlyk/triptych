GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt test lint e2e e2e-real-claude

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

lint:
	$(GOLANGCI_LINT) run ./...

e2e:
	python3 scripts/e2e_smoke.py

e2e-real-claude:
	python3 scripts/e2e_smoke.py --mode real-claude
