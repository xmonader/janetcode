BIN := janetcode
PKG := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test race lint check clean dist run eval eval-baseline eval-train cross harness-bench harness-bench-large

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/$(BIN)

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/$(BIN)

test:
	go test $(PKG)

# Golden corpus for the web markdown renderer: security (no script /
# javascript: hrefs), the inline-code pill regression, fences, tables,
# streaming prefixes. Pure node, no browser.
md-test:
	node internal/webui/uitest/md_test.mjs

# WCAG contrast ratios for every text/background pair the web palette
# uses, light and dark. Pure node.
ui-a11y:
	node internal/webui/uitest/a11y.mjs

race:
	go test -race $(PKG)

# staticcheck is optional locally (go install honnef.co/go/tools/cmd/staticcheck@latest)
# but the gate fails on its findings whenever it is installed, so CI and a
# developer with it on PATH see the same bar.
STATICCHECK := $(shell command -v staticcheck 2>/dev/null || ls $(HOME)/go/bin/staticcheck 2>/dev/null)

lint:
	go vet $(PKG)
	@if [ -n "$(STATICCHECK)" ]; then $(STATICCHECK) $(PKG); else echo "staticcheck not installed; skipping"; fi

# The ordinary gate: formatting, vet, the tests, and the other two
# platforms compile-checked. The cross-compile belongs here rather than
# only in `cross`, because a build-tagged file that compiles on one OS
# and not another — bash_windows.go and its neighbours — is otherwise
# found by whoever happens to run `make cross`, which is nobody, and
# then by a user. `gofmt -l` prints and exits 0, so it is gated by hand.
check: lint test md-test ui-a11y
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
	GOOS=windows GOARCH=amd64 go vet $(PKG)
	GOOS=windows GOARCH=amd64 go build $(PKG)
	GOOS=darwin GOARCH=arm64 go vet $(PKG)

# Live behaviour tests against the configured provider (costs money).
# EVAL_ONLY=go,files narrows by task name or tag; EVAL_JOBS sets parallelism;
# EVAL_RUNS repeats each task; EVAL_OFF=coach,prune ablates behaviours;
# EVAL_ROUTE=<model> sets route_cheap for the run (experimental step
# routing, see docs/manual/evals.md); EVAL_COMPARE=evals/baseline.json
# prints a paired comparison.
eval: build
	./bin/$(BIN) --eval evals --eval-jobs $${EVAL_JOBS:-2} --eval-runs $${EVAL_RUNS:-1} \
		$${EVAL_ONLY:+--eval-only $$EVAL_ONLY} $${EVAL_OFF:+--eval-off $$EVAL_OFF} \
		$${EVAL_ROUTE:+--eval-route $$EVAL_ROUTE} \
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

# Browser-level UI checks: boots the webui against a stub provider in a
# scratch config and drives it with headless Chrome over CDP — toggles,
# drag-resize, drawer stacking. Needs node >= 22 (global WebSocket) and
# a chrome binary (CHROME=/path/to/chrome if not on PATH).
UI_PORT ?= 18991
CHROME ?= google-chrome
ui-test: build
	@tmp=$$(mktemp -d); mkdir -p $$tmp/janetcode; \
	printf '%s' '{"provider":"stub","model":"stub/stub-model","providers":{"stub":{"type":"openai","base_url":"http://127.0.0.1:9/v1","api_key":"sk-test","models":["stub-model"]}}}' > $$tmp/janetcode/config.json; \
	XDG_CONFIG_HOME=$$tmp XDG_DATA_HOME=$$tmp ./bin/janetcode --web --web-addr 127.0.0.1:$(UI_PORT) >/dev/null 2>&1 & \
	srv=$$!; \
	for i in $$(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:$(UI_PORT)/ && break; sleep 0.5; done; \
	rm -rf /tmp/janetcode-chrome-cdp; \
	$(CHROME) --headless=new --no-sandbox --disable-gpu --window-size=1440,900 --remote-debugging-port=9223 --user-data-dir=/tmp/janetcode-chrome-cdp about:blank >/dev/null 2>&1 & \
	chr=$$!; \
	node internal/webui/uitest/interact.mjs http://127.0.0.1:$(UI_PORT)/; \
	rc=$$?; kill $$srv $$chr 2>/dev/null; wait $$srv $$chr 2>/dev/null; rm -rf $$tmp /tmp/janetcode-chrome-cdp; exit $$rc

# First run in a real browser: --web with no config and no key opens on
# the connect form; a rejected key is said, a good one connects and is
# saved. env -i keeps the developer's own API keys out of the run.
UI_FIRSTRUN_PORT ?= 18993
ui-firstrun: build
	@tmp=$$(mktemp -d); \
	env -i HOME=$$tmp PATH=$$PATH XDG_CONFIG_HOME=$$tmp XDG_DATA_HOME=$$tmp ./bin/janetcode --web --web-addr 127.0.0.1:$(UI_FIRSTRUN_PORT) </dev/null >$$tmp/server.log 2>&1 & \
	srv=$$!; \
	for i in $$(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:$(UI_FIRSTRUN_PORT)/ && break; sleep 0.5; done; \
	$(CHROME) --headless=new --no-sandbox --disable-gpu --window-size=1440,900 --remote-debugging-port=9225 --user-data-dir=$$tmp/chrome about:blank >/dev/null 2>&1 & \
	chr=$$!; \
	node internal/webui/uitest/firstrun.mjs http://127.0.0.1:$(UI_FIRSTRUN_PORT)/ 9225 18994 $$tmp/janetcode/config.json; \
	rc=$$?; [ $$rc = 0 ] || cat $$tmp/server.log; kill $$srv $$chr 2>/dev/null; wait $$srv $$chr 2>/dev/null; rm -rf $$tmp 2>/dev/null; exit $$rc

# Real turns in a real browser: the server talks to a scripted model
# (turn.mjs starts it), so the permission round trip, the file written by
# the real tool and the /events replay after a reload are all exercised.
UI_TURN_PORT ?= 18995
UI_TURN_MODEL_PORT ?= 18996
ui-turn: build
	@tmp=$$(mktemp -d); mkdir -p $$tmp/janetcode $$tmp/work; \
	printf '%s' '{"provider":"scripted","model":"scripted/turn-model","providers":{"scripted":{"type":"openai","base_url":"http://127.0.0.1:$(UI_TURN_MODEL_PORT)/v1","api_key":"sk-test","models":["turn-model"]}}}' > $$tmp/janetcode/config.json; \
	(cd $$tmp/work && env -i HOME=$$tmp PATH=$$PATH XDG_CONFIG_HOME=$$tmp XDG_DATA_HOME=$$tmp XDG_STATE_HOME=$$tmp $(CURDIR)/bin/janetcode --web --web-addr 127.0.0.1:$(UI_TURN_PORT) </dev/null >$$tmp/server.log 2>&1) & \
	srv=$$!; \
	for i in $$(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:$(UI_TURN_PORT)/ && break; sleep 0.5; done; \
	$(CHROME) --headless=new --no-sandbox --disable-gpu --window-size=1440,900 --remote-debugging-port=9226 --user-data-dir=$$tmp/chrome about:blank >/dev/null 2>&1 & \
	chr=$$!; \
	node internal/webui/uitest/turn.mjs http://127.0.0.1:$(UI_TURN_PORT)/ 9226 $(UI_TURN_MODEL_PORT) $$tmp/work; \
	rc=$$?; [ $$rc = 0 ] || tail -40 $$tmp/server.log; pkill -P $$srv 2>/dev/null; kill $$srv $$chr 2>/dev/null; wait $$srv $$chr 2>/dev/null; rm -rf $$tmp 2>/dev/null; exit $$rc

# Visual regression gate: boots the webui like ui-test, shoots every state
# shot.mjs knows (fed from a fake event stream, so the pixels are the same
# every run), and compares each with internal/webui/uitest/baseline/.
# A changed state fails and leaves a .diff.png beside the new shot.
# make ui-visual UPDATE=1 accepts the new shots as the baseline.
UI_VISUAL_PORT ?= 18992
ui-visual: build
	@tmp=$$(mktemp -d); mkdir -p $$tmp/janetcode $$tmp/shots; \
	printf '%s' '{"provider":"stub","model":"stub/stub-model","providers":{"stub":{"type":"openai","base_url":"http://127.0.0.1:9/v1","api_key":"sk-test","models":["stub-model"]}}}' > $$tmp/janetcode/config.json; \
	XDG_CONFIG_HOME=$$tmp XDG_DATA_HOME=$$tmp ./bin/janetcode --web --web-addr 127.0.0.1:$(UI_VISUAL_PORT) >/dev/null 2>&1 & \
	srv=$$!; \
	for i in $$(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:$(UI_VISUAL_PORT)/ && break; sleep 0.5; done; \
	$(CHROME) --headless=new --no-sandbox --disable-gpu --hide-scrollbars --force-device-scale-factor=1 --window-size=1440,900 --remote-debugging-port=9224 --user-data-dir=$$tmp/chrome about:blank >/dev/null 2>&1 & \
	chr=$$!; \
	node internal/webui/uitest/shot.mjs http://127.0.0.1:$(UI_VISUAL_PORT)/ $$tmp/shots 9224 >/dev/null && \
	node internal/webui/uitest/shotdiff.mjs 9224 $$tmp/shots internal/webui/uitest/baseline $(if $(UPDATE),--update); \
	rc=$$?; kill $$srv $$chr 2>/dev/null; wait $$srv $$chr 2>/dev/null; \
	if [ $$rc -ne 0 ]; then mkdir -p ui-shots && cp $$tmp/shots/* ui-shots/ && echo "new shots and diffs copied to ui-shots/"; fi; \
	rm -rf $$tmp 2>/dev/null; exit $$rc

# Screenshot the RUNNING webui (UI_URL) into ui-shots/ for visual review.
ui-shot:
	@mkdir -p ui-shots; rm -rf /tmp/janetcode-chrome-cdp; \
	$(CHROME) --headless=new --no-sandbox --disable-gpu --window-size=1440,900 --remote-debugging-port=9223 --user-data-dir=/tmp/janetcode-chrome-cdp about:blank >/dev/null 2>&1 & \
	chr=$$!; \
	node internal/webui/uitest/shot.mjs $(or $(UI_URL),http://127.0.0.1:8787/) ui-shots; \
	rc=$$?; kill $$chr 2>/dev/null; wait $$chr 2>/dev/null; rm -rf /tmp/janetcode-chrome-cdp; exit $$rc

clean:
	rm -rf bin dist

# Release binaries for every platform janetcode ships on, pure Go (the
# tree-sitter build needs cgo and stays a from-source option), with a
# SHA256SUMS file to check them against. make dist VERSION=v0.3.0
DIST_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
dist:
	rm -rf dist && mkdir -p dist
	@set -e; for t in $(DIST_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		out=dist/$(BIN)-$(VERSION)-$$os-$$arch$$ext; \
		echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/$(BIN); \
	done
	cd dist && sha256sum $(BIN)-* > SHA256SUMS

run: build
	./bin/$(BIN)
