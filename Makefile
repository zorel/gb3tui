GO ?= go

.PHONY: build test fmt

build:
	$(GO) build -o gb3tui .

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...
