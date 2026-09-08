BIN := janetcode
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test race lint clean run eval

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/$(BIN)

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/$(BIN)

test:
	go test $(PKG)

race:
	go test -race $(PKG)

lint:
	go vet $(PKG)

# Live behaviour tests against the configured provider (costs money).
# EVAL_ONLY=go,files narrows by task name or tag; EVAL_JOBS sets parallelism.
eval: build
	./bin/$(BIN) --eval evals --eval-jobs $${EVAL_JOBS:-2} $${EVAL_ONLY:+--eval-only $$EVAL_ONLY} --eval-json evals/last-results.json

clean:
	rm -rf bin

run: build
	./bin/$(BIN)
