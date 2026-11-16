# Skein developer commands. Every target works without the cloud; only the
# stack targets (up, up-min, down, logs, stack-smoke) need Docker.

GO_MODULES := $(shell find libs services -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; 2>/dev/null | sort)
FUZZTIME   ?= 30s
COMPOSE    := docker compose -f deploy/compose/docker-compose.yml
# Libraries that must stay buildable for the browser (ADR-0007).
WASM_LIBS  := libs/crdt

.PHONY: help test test-lib vet fmt-check fuzz bench wasm-check proto-check up up-min down logs urls stack-check stack-smoke ci

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
	  for t in $$(cd $$m && go test -list '^Fuzz' ./... | grep '^Fuzz'); do \
	    echo "== $$m $$t"; (cd $$m && go test -run='^$$' -fuzz="^$$t$$" -fuzztime=$(FUZZTIME) .) || exit 1; \
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

# gen/openapi is created first so Docker does not create it as root for the
# Swagger UI mount; it stays empty until the contracts pipeline generates it.
up: ## Start infrastructure plus observability (telemetry, dashboards, API docs)
	@mkdir -p gen/openapi
	$(COMPOSE) --profile observability up -d --wait
	@$(MAKE) --no-print-directory urls

up-min: ## Start only infrastructure: Postgres, Redis, Pub/Sub emulator, Mailpit
	$(COMPOSE) up -d --wait

down: ## Stop the local stack, every profile (keeps data)
	$(COMPOSE) --profile '*' down

logs: ## Follow the local stack's logs
	$(COMPOSE) --profile '*' logs -f

urls: ## Print every local URL
	@echo "Infrastructure (make up-min and make up)"
	@echo "  Postgres            postgres://skein:skein@localhost:5432/skein"
	@echo "  Redis               redis://localhost:6379"
	@echo "  Pub/Sub emulator    http://localhost:8085  (PUBSUB_EMULATOR_HOST=localhost:8085)"
	@echo "  Mailpit             http://localhost:8025  (SMTP localhost:1025)"
	@echo "Observability (make up)"
	@echo "  Grafana             http://localhost:3000  (dashboards in the Skein folder)"
	@echo "  Jaeger              http://localhost:16686"
	@echo "  Prometheus          http://localhost:9090"
	@echo "  Loki API            http://localhost:3100  (browse logs in Grafana > Explore)"
	@echo "  Swagger UI          http://localhost:8090  (spec from gen/openapi)"
	@echo "  Redis Insight       http://localhost:5540"
	@echo "  OTLP gRPC           localhost:4317         (OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317)"
	@echo "  OTLP HTTP           http://localhost:4318"

stack-check: ## Validate compose, collector, Jaeger, Prometheus, Loki and dashboard configs
	deploy/observability/check.sh

SMOKE := deploy/observability/smoke
stack-smoke: ## Send synthetic telemetry to a running `make up` stack and check dashboards and redaction
	@test -x $(SMOKE)/.venv/bin/python || (python3 -m venv $(SMOKE)/.venv && $(SMOKE)/.venv/bin/pip install -q -r $(SMOKE)/requirements.txt)
	$(SMOKE)/.venv/bin/python $(SMOKE)/emit.py --seconds 60
	@sleep 10 # let the collector's batch and Prometheus' remote-write ingestion catch up
	python3 $(SMOKE)/check.py

ci: fmt-check vet test wasm-check ## What CI runs for Go
