.PHONY: test test-race test-integration vet demo up down simulate report wasm web build

ADMIN_URL ?= postgres://tollgate:tollgate_dev@localhost:5432/tollgate

test:             ## unit tests, no infrastructure
	go test ./...

test-race:
	go test -race ./...

test-integration: ## Postgres tests (needs `make up`, or any Postgres with the dev roles)
	TOLLGATE_TEST_ADMIN_URL=$(ADMIN_URL) go test -count=1 -v ./internal/store/...

vet:
	go vet ./...
	gofmt -l . | (! grep .)

demo:             ## run a scenario in memory and print every verdict
	go run ./cmd/tollgate demo

up:               ## Postgres, Redpanda, Console, setup, auditor, outbox relay
	docker compose up -d --build

down:
	docker compose down

simulate:         ## produce a synthetic scenario to Redpanda
	docker compose run --rm simulate

report:           ## per-tenant totals, read through row-level security
	docker compose run --rm report

wasm:
	sh web/scripts/build-wasm.sh

web:              ## browser demo on http://localhost:5180/Tollgate/
	cd web && npm run dev

build:
	go build -o bin/tollgate ./cmd/tollgate
	cd web && npm run build
