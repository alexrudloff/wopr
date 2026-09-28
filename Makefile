# WOPR's command interface. Developers, CI, and coding agents run make targets
# instead of ad hoc shell commands. `make help` lists the targets by group.
#
# Every script behind these targets lives in automation/ (see its README):
# dev/ for setup and local tools, ci/ for gates and test runners, gen/ for
# generators, release/ for release checks, and make/ for
# the CI shard targets included at the end of this file.

.DEFAULT_GOAL := help
# Caches follow XDG_CACHE_HOME; temporary files follow TMPDIR. Sandboxes,
# devcontainers, and hosted runners often forbid writes elsewhere.
WOPR_CACHE_HOME ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)
# golangci-lint caches absolute package paths. Isolate each checkout so parallel worktrees retain warm results without serving findings from another tree.
GOLANGCI_LINT_CACHE ?= $(WOPR_CACHE_HOME)/golangci-lint/$(notdir $(CURDIR))
export GOLANGCI_LINT_CACHE
WOPR_TMP ?= $(patsubst %/,%,$(or $(TMPDIR),/tmp))
# macOS mktemp ignores TMPDIR without a template, so recipes always pass one.
MKTEMP = mktemp "$(WOPR_TMP)/wopr.XXXXXXXX"
MKTEMP_DIR = mktemp -d "$(WOPR_TMP)/wopr.XXXXXXXX"
WOPR_DEV_HOME ?= $(WOPR_CACHE_HOME)/wopr-dev
# Machine-local toolchain and module-proxy settings written by `make setup`.
# Absent on machines that need neither.
-include $(WOPR_DEV_HOME)/env.mk

WOPR_BIN ?= $(HOME)/.local/bin/wopr
SETUP_ARGS ?=
COMPLIANCE_ARGS ?=
RUNS ?= 10
MODE ?=
MODEL ?=
TASKS ?= all
EVAL_RESULTS ?= tmp/evals/overhead.json
LIVE_RUNS ?= 3
LIVE_PARALLEL ?= 1
EVAL_TASKS_DIR ?=
LIVE_RESULTS ?=
MUTATIONS ?= 30
SEED ?= 1
PROFILE ?= cpu,heap
BENCH ?= .
BENCH_PKGS ?= ./agent/... ./tui/... ./internal/codingagent/...
BENCH_COUNT ?= 6
SLOP_MAX_VERBOSITY = $(shell sed -n 's/^max_verbosity *= *//p' evals/budgets.toml)
SLOP_MAX_EROSION = $(shell sed -n 's/^max_erosion *= *//p' evals/budgets.toml)
WOPR_EVAL := go run ./cmd/wopr-eval
HELP_AWK := BEGIN {FS = ":.*\#\# "} /^\#\#@ / {printf "\n%s\n", substr($$0, 5); next} /^[a-zA-Z0-9_%-]+:.*\#\# / {printf "  %-24s %s\n", $$1, $$2}

##@ Start here

setup: ## Install pinned Go and bin/wopr, and write the make and shell environment
	@./automation/dev/setup.sh $(SETUP_ARGS)

doctor: ## Report every development prerequisite without changing anything
	@./automation/dev/setup.sh --check $(SETUP_ARGS)

help: ## List targets by group
	@awk '$(HELP_AWK)' Makefile
	@echo
	@echo "Scripts: automation/README.md."

##@ Build

wopr: ## Build bin/wopr from this checkout, with symbols for profiling
	@mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-X main.Build=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o bin/wopr ./cmd/wopr

clean: ## Remove bin/ and tmp/ (builds, eval results, profiles, benchmarks)
	rm -rf bin tmp

build: ## Compile every package (go build ./...)
	go build -buildvcs=false ./...

# install: build wopr binary → ~/.local/bin/wopr (or WOPR_BIN override)
# with embedded git sha + macOS ad-hoc codesign.
install: ## Install a stripped wopr to WOPR_BIN (default ~/.local/bin/wopr)
	@mkdir -p $(dir $(WOPR_BIN))
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.Build=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o $(WOPR_BIN) ./cmd/wopr
	@[ "$$(uname -s)" = "Darwin" ] && command -v codesign >/dev/null 2>&1 && codesign --force --sign - $(WOPR_BIN) >/dev/null 2>&1 || true
	@echo "installed: $$($(WOPR_BIN) --version) → $(WOPR_BIN)"

##@ Test and lint

vet: ## Run go vet on every package
	go vet ./...

LINT_BASE ?= main

lint: ## Run golangci-lint over the repository (part of make check)
	@echo "Running golangci-lint..."
	go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live ./...

lint-changed: ## Lint whole changed Go files in changed packages against LINT_BASE (default main)
	@base=$$(git merge-base HEAD "$(LINT_BASE)") || { echo "lint-changed: cannot find merge base with $(LINT_BASE)" >&2; exit 2; }; \
	changed_go=$$({ git diff --name-only "$$base"...HEAD; git diff --name-only; git diff --name-only --cached; git ls-files --others --exclude-standard; } | awk '/\.go$$/' | sort -u); \
	if [ -z "$$changed_go" ]; then echo "lint-changed: no changed Go files"; exit 0; fi; \
	packages=$$(printf '%s\n' "$$changed_go" | while IFS= read -r file; do dir=$${file%/*}; [ "$$dir" = "$$file" ] && dir=.; [ -d "$$dir" ] && printf './%s\n' "$$dir"; done | sort -u); \
	if [ -z "$$packages" ]; then echo "lint-changed: no changed Go packages"; exit 0; fi; \
	echo "Running golangci-lint on changed packages: $$(printf '%s' "$$packages" | tr '\n' ' ')"; \
	go tool golangci-lint run --allow-parallel-runners --build-tags=integration,live --new-from-rev="$$base" --whole-files $$packages

# Fast inner-loop gate. Use between edits; `make check` is the pre-commit gate.
dev: build vet test-fast ## Fast inner loop: build, vet, and the fast test group

test: test-prereqs ## Grouped Go tests with safe concurrency
	@./automation/ci/test-grouped.sh

test-prereqs: ## Require every toolchain the core suite exercises
	@missing=""; \
	for tool in go git tmux; do \
		command -v "$$tool" >/dev/null 2>&1 || missing="$$missing $$tool"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "missing required wopr test toolchains:$$missing" >&2; \
		exit 1; \
	fi
	@bash -c '[ "$${BASH_VERSINFO[0]}" -ge 4 ]' || { echo "the test scripts need bash 4 or newer on PATH (macOS: brew install bash)" >&2; exit 1; }
	@echo "test prerequisites: go, git, tmux, bash 4+"

test-stress: ## Stress the grouped Go test scheduler under higher contention
	@./automation/ci/test-grouped.sh stress

# Deterministic tmux-driven integration tier: real wopr in a real pty, faux
# provider, no network. Carries the signal guards, which cover shutdown paths
# no unit test reaches (SIGINT aborting the operation rather than the
# session).
test-integration: ## Deterministic tmux tier: real wopr, faux provider, no network
	@./automation/ci/integration-tests.sh fast

# Race + goroutineleak gate for the interactive TUI concurrency model. Locks in
# the single-main-loop ownership invariant: a future off-loop UI touch fails
# here instead of shipping a latent data race. See automation/ci/test-race.sh.
test-race: ## Race and goroutine-leak gate for the TUI concurrency model
	@./automation/ci/test-race.sh

# go-fix-clean enforces AGENTS.md rule #3 (Go 1.27 idiom compliance).
# `go fix -diff ./...` must produce zero output. If this target fails,
# run `go fix ./...`, manually validate any suggestions that would
# change observable behavior (e.g. type changes like
# `slices.Contains` returning bool vs a string flag), then commit.
go-fix-clean: ## Fail when go fix would change any file
	@# Judge the diff on stdout; stderr (tool warnings) passes through. go fix
	@# exits non-zero when it has a diff, so a failure without one is an error.
	@status=0; diff_output=$$(go fix -diff ./...) || status=$$?; \
	if [ -n "$$diff_output" ]; then \
		echo "FAIL: go fix -diff produced output (AGENTS.md rule #3)."; \
		echo "Run \`go fix ./...\` and validate suggestions before committing."; \
		echo "First 40 lines of diff:"; \
		echo "$$diff_output" | head -40; \
		exit 1; \
	fi; \
	if [ "$$status" -ne 0 ]; then echo "FAIL: go fix -diff exited $$status without a diff."; exit 1; fi
	@echo "go fix: clean (Go 1.27 idioms compliant)"

# docs-drift gates the shipped docs bundle against the code it describes: every
# documented command must exist, every listable command must be documented, and
# every page must be reachable from the index.
docs-drift: ## Fail when generated documentation no longer matches its source
	@go test ./tests/docs-drift/ -count=1

##@ Gates

check: build vet lint test test-race test-integration go-fix-clean docs-drift ## Pre-commit gate: build, lint, unit, race, integration, and docs gates
	@echo
	@echo "make check: all gates green."

##@ Evals and performance

evals: wopr ## Measure wopr's overhead (startup, round trips per protocol, resumed Session) against a mock model (RUNS=10)
	@$(WOPR_EVAL) overhead --runs $(RUNS) --out $(EVAL_RESULTS)

evals-live: wopr ## Run evals/tasks with real models: MODE=auto,speed,quality and/or MODEL=provider/model,... (LIVE_RUNS=3, LIVE_PARALLEL=1, TASKS=all)
	@$(WOPR_EVAL) live --mode "$(MODE)" --model "$(MODEL)" --runs $(LIVE_RUNS) --parallel $(LIVE_PARALLEL) --tasks "$(TASKS)" $(if $(EVAL_TASKS_DIR),--tasks-dir $(EVAL_TASKS_DIR)) --out tmp/evals/live.json

evals-mutate: ## Generate seeded bug-fix tasks from wopr's Go source (MUTATIONS=30 SEED=1) into tmp/evals/mutation-tasks
	@$(WOPR_EVAL) mutate --count $(MUTATIONS) --seed $(SEED) --out tmp/evals/mutation-tasks

evals-publish: ## Copy a reviewed EVAL_RESULTS run (and LIVE_RESULTS, if set) to evals/results and regenerate docs/site/docs/evals.md
	@test -f "$(EVAL_RESULTS)" || { echo "evals-publish: $(EVAL_RESULTS) not found; run make evals first" >&2; exit 2; }
	@$(WOPR_EVAL) publish --overhead "$(EVAL_RESULTS)" $(if $(LIVE_RESULTS),--live "$(LIVE_RESULTS)")

evals-test: ## Unit-test the eval harness (internal/evals, cmd/wopr-eval)
	@go test ./internal/evals/ ./cmd/wopr-eval/

perf-check: ## Apply evals/budgets.toml (latency, memory, and request ceilings) to EVAL_RESULTS
	@$(WOPR_EVAL) check $(EVAL_RESULTS)

profile: wopr ## Profile one prompt round trip (PROFILE=cpu,heap,allocs,block,mutex,trace) into tmp/profile
	@$(WOPR_EVAL) profile --kinds $(PROFILE) --out tmp/profile

pgo: wopr ## Merge CPU profiles from repeated round trips into tmp/pgo/default.pgo for a profile-guided build
	@$(WOPR_EVAL) profile --kinds cpu --runs 20 --out tmp/pgo --merge tmp/pgo/default.pgo

slop: ## Measure erosion and clone verbosity (SlopCodeBench metrics) and list the heaviest functions
	@go run ./automation/ci/slopmetrics -top 15

slop-check: ## Fail when erosion or verbosity exceed the ratchet in evals/budgets.toml [slop]
	@go run ./automation/ci/slopmetrics -top 0 -max-verbosity $(SLOP_MAX_VERBOSITY) -max-erosion $(SLOP_MAX_EROSION) >/dev/null && echo "slop-check: within verbosity $(SLOP_MAX_VERBOSITY), erosion $(SLOP_MAX_EROSION)"

bench: ## Run Go benchmarks into tmp/bench/current.txt (BENCH=regex, BENCH_PKGS, BENCH_COUNT=6)
	@mkdir -p tmp/bench
	@go test -run '^$$' -bench '$(BENCH)' -benchmem -count $(BENCH_COUNT) $(BENCH_PKGS) > tmp/bench/current.txt; status=$$?; cat tmp/bench/current.txt; exit $$status

bench-base: ## Keep the last benchmark run as the base for bench-compare
	@cp tmp/bench/current.txt tmp/bench/base.txt

bench-compare: ## Compare the last benchmark run with the base by median; fails above a 5% regression
	@$(WOPR_EVAL) benchcmp --fail tmp/bench/base.txt tmp/bench/current.txt

##@ Docs

knowledge-graph: ## Regenerate the knowledge graph page, JSON-LD, and Mermaid from docs/knowledge-graph/wopr.graph.json
	@go run ./automation/gen/knowledgegraph

.PHONY: bench bench-base bench-compare build check clean dev docs-drift doctor evals evals-live evals-mutate evals-publish evals-test go-fix-clean help install knowledge-graph lint lint-changed perf-check pgo profile setup slop slop-check test test-integration test-prereqs test-race test-stress vet wopr

include automation/make/ci.mk
