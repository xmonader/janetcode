BIN := janetcode
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test race lint clean run eval eval-baseline cross

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
# EVAL_ONLY=go,files narrows by task name or tag; EVAL_JOBS sets parallelism;
# EVAL_RUNS repeats each task; EVAL_OFF=coach,prune ablates behaviours;
# EVAL_COMPARE=evals/baseline.json prints a paired comparison.
eval: build
	./bin/$(BIN) --eval evals --eval-jobs $${EVAL_JOBS:-2} --eval-runs $${EVAL_RUNS:-1} \
		$${EVAL_ONLY:+--eval-only $$EVAL_ONLY} $${EVAL_OFF:+--eval-off $$EVAL_OFF} \
		$${EVAL_COMPARE:+--eval-compare $$EVAL_COMPARE} $${EVAL_LABEL:+--eval-label $$EVAL_LABEL} \
		--eval-json $${EVAL_JSON:-evals/last-results.json}

# Save the last run as the baseline that later runs compare against.
eval-baseline:
	cp evals/last-results.json evals/baseline.json

# Cross-compile checks; Windows and macOS are built, not run, here.
cross:
	GOOS=windows GOARCH=amd64 go build ./...
	GOOS=darwin GOARCH=arm64 go build ./...

clean:
	rm -rf bin

run: build
	./bin/$(BIN)
