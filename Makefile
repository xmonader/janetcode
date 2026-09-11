BIN := janetcode
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test race lint check clean run eval eval-baseline eval-train cross harness-bench harness-bench-large

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

# The ordinary gate: formatting, vet, the tests, and the other two
# platforms compile-checked. The cross-compile belongs here rather than
# only in `cross`, because a build-tagged file that compiles on one OS
# and not another — bash_windows.go and its neighbours — is otherwise
# found by whoever happens to run `make cross`, which is nobody, and
# then by a user. `gofmt -l` prints and exits 0, so it is gated by hand.
check: lint test
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
	GOOS=windows GOARCH=amd64 go vet $(PKG)
	GOOS=windows GOARCH=amd64 go build $(PKG)
	GOOS=darwin GOARCH=arm64 go vet $(PKG)

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

## Train the trajectory critic from eval result files (FILES=a.json,b.json or a directory).
eval-train: build
	./bin/janetcode --eval-train $(or $(FILES),evals)

# With tree-sitter grammars compiled in: the repository map then reads
# Python, JavaScript, TypeScript, Rust and C with real parsers instead of
# regular expressions. Needs cgo and a C toolchain, which is why it is not
# the default build.
build-treesitter:
	CGO_ENABLED=1 go build -tags treesitter -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/$(BIN)

test-treesitter:
	CGO_ENABLED=1 go test -tags treesitter ./internal/treesitter/ ./internal/repomap/

# Cross-compile checks; Windows and macOS are built, not run, here.
cross:
	GOOS=windows GOARCH=amd64 go build ./...
	GOOS=darwin GOARCH=arm64 go build ./...

# Live 01–20 × six-harness benchmark (needs DASHSCOPE_API_KEY or OPENAI_API_KEY).
harness-bench: build
	go test ./internal/eval -count=1 -run 'TestBatchesOfFive|TestAggregate'
	go build -o bin/harnessbench ./cmd/harnessbench
	./bin/harnessbench --evals evals --out docs/benchmarks/harnesses

# Five large evals (~1M cumulative tokens) × five harnesses (no openclaw).
harness-bench-large: build
	go test ./internal/eval -count=1 -run 'TestLarge'
	go build -o bin/harnessbench ./cmd/harnessbench
	./bin/harnessbench --filter large --evals evals/large --out docs/benchmarks/harnesses/large --timeout 20m --janetcode bin/janetcode

clean:
	rm -rf bin

run: build
	./bin/$(BIN)
