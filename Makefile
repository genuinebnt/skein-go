# Skein developer commands. Every target works without the cloud; only `up`,
# `down` and `logs` need Docker.

GO_MODULES := $(shell find libs services platform -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null | sort)
FUZZTIME   ?= 30s
COMPOSE    := docker compose -f deploy/compose/docker-compose.yml
# Libraries that must stay buildable for the browser (ADR-0007).
WASM_LIBS  := libs/crdt

.PHONY: help test test-lib vet fmt-check fuzz bench wasm-check proto-check up down logs ci

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

proto-check: ## Lint contracts and check for breaking changes against main
	cd proto && buf lint
	@if git cat-file -e origin/main:proto/buf.yaml 2>/dev/null; then \
	  cd proto && buf breaking --against '../.git#branch=origin/main,subdir=proto'; \
	else echo "no contracts on main yet; skipping breaking check"; fi

up: ## Start the local stack
	$(COMPOSE) up -d --wait

down: ## Stop the local stack (keeps data)
	$(COMPOSE) down

logs: ## Follow the local stack's logs
	$(COMPOSE) logs -f

ci: fmt-check vet test wasm-check ## What CI runs for Go
