# Skein developer commands. Every target works without the cloud; only `up`,
# `down` and `logs` need Docker.

GO_MODULES := $(shell find libs services gen platform tools -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null | sort)
FUZZTIME   ?= 30s
COMPOSE    := docker compose -f deploy/compose/docker-compose.yml
# Libraries that must stay buildable for the browser (ADR-0007).
WASM_LIBS  := libs/crdt

# Seed data (tools/seed, docs/seed-data.md). bin/ is git-ignored.
PROFILE    ?= medium
SEED       ?= 1234
SEED_OUT   ?= bin/seed/$(PROFILE)

.PHONY: help test test-lib vet fmt-check fuzz bench wasm-check proto proto-tools proto-check proto-stale up down logs ci seed seed-small seed-check

help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

test: ## Run every Go module's tests with the race detector
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test -race -count=1 ./...) || exit 1; done

test-lib: ## Test one library: make test-lib LIB=crdt
	cd libs/$(LIB) && go test -race -count=1 -v ./...

vet: ## go vet every module
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l $(GO_MODULES)); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@for m in $(GO_MODULES); do \
	  for p in $$(cd $$m && go list ./...); do \
	    for t in $$(cd $$m && go test -list '^Fuzz' $$p | grep '^Fuzz'); do \
	      echo "== $$p $$t"; (cd $$m && go test -run='^$$' -fuzz="^$$t$$" -fuzztime=$(FUZZTIME) $$p) || exit 1; \
	    done; \
	  done; \
	done

bench: ## Run benchmarks
	@for m in $(GO_MODULES); do (cd $$m && go test -run='^$$' -bench=. -benchmem ./...) || exit 1; done

wasm-check: ## Build browser-bound libraries for js/wasm and wasip1
	@for m in $(WASM_LIBS); do \
	  (cd $$m && GOOS=js GOARCH=wasm go build ./... && GOOS=wasip1 GOARCH=wasm go build ./...) || exit 1; \
	  echo "$$m builds for wasm"; \
	done

# Code generation (buf.gen.yaml, scripts/gen-python.sh). The plugin versions
# are pinned here, in buf.gen.yaml and in CI; generated code is committed, and
# CI fails if it differs from what these versions produce.
BUF_VERSION                := v1.47.2
PROTOC_GEN_GO_VERSION      := v1.35.2
PROTOC_GEN_GO_GRPC_VERSION := v1.5.1
GRPC_GATEWAY_VERSION       := v2.24.0
GRPCIO_TOOLS_VERSION       := 1.85.1

proto-tools: ## Install the pinned buf, protoc plugins and grpcio-tools
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@$(GRPC_GATEWAY_VERSION)
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@$(GRPC_GATEWAY_VERSION)
	python3 -m pip install grpcio-tools==$(GRPCIO_TOOLS_VERSION)

proto: ## Format and lint contracts, then generate Go, OpenAPI and Python into gen/
	cd proto && buf format -w --path skein && buf lint
	@# Generated directories are removed first so a deleted proto leaves no
	@# stale output; gen/go/go.mod and the hand-written tests are kept.
	rm -rf gen/go/skein gen/openapi
	buf generate
	./scripts/gen-python.sh

# Base for the breaking-change check; CI and local runs default to main.
PROTO_BASE ?= origin/main

proto-check: ## Check formatting, lint and breaking changes against PROTO_BASE
	cd proto && buf format -d --exit-code --path skein
	cd proto && buf lint
	@# Both sides are built to single images and compared on skein/ only, so
	@# the check works across changes to the module layout (the vendored
	@# third_party module) and ignores upgrades of vendored files.
	@if git cat-file -e $(PROTO_BASE):proto/buf.yaml 2>/dev/null; then \
	  tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && cd proto && \
	  buf build -o "$$tmp/head.binpb" && \
	  buf build '../.git#ref=$(PROTO_BASE),subdir=proto' -o "$$tmp/base.binpb" && \
	  buf breaking "$$tmp/head.binpb" --against "$$tmp/base.binpb" --path skein && \
	  echo "no breaking changes against $(PROTO_BASE)"; \
	else echo "no contracts on $(PROTO_BASE) yet; skipping breaking check"; fi

proto-stale: proto ## Regenerate and fail if the committed gen/ differs
	@git add -N gen && git diff --exit-code --stat gen || \
	  { echo "gen/ is stale: run 'make proto' and commit the result"; exit 1; }

up: ## Start the local stack
	$(COMPOSE) up -d --wait

down: ## Stop the local stack (keeps data)
	$(COMPOSE) down

logs: ## Follow the local stack's logs
	$(COMPOSE) logs -f

seed: ## Generate seed data: make seed PROFILE=small|medium|large|stress [SEED=1234] [SEED_OUT=dir]
	go run ./tools/seed -profile $(PROFILE) -seed $(SEED) -out $(SEED_OUT)

seed-small: ## Regenerate the committed small dataset in testdata/seed/small
	go run ./tools/seed -profile small -seed 1234 -out testdata/seed/small

seed-check: ## Seed tool tests, including the medium profile's time budget
	cd tools/seed && SKEIN_SEED_MEDIUM=1 go test -count=1 -timeout 20m ./...

ci: fmt-check vet test wasm-check ## What CI runs for Go
