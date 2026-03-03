# ─────────────────────────────────────────────────────────────
# Makefile — aarpaar-backend test targets
# ─────────────────────────────────────────────────────────────

.PHONY: test test-unit test-integration test-security test-bench test-all \
        test-coverage lint vet load-rest load-ws load-spike load-soak \
        test-db-create test-db-drop clean

# ── Config ──────────────────────────────────────────────────
GO        ?= go
K6        ?= k6
BASE_URL  ?= http://localhost:2028
TEST_DB   ?= aarpaar_test
PG_USER   ?= postgres
PG_PASS   ?= root
PG_HOST   ?= localhost
PG_PORT   ?= 5432
REDIS_PORT?= 6378
TIMEOUT   ?= 5m

export TEST_POSTGRES_CONN_STR ?= postgresql://$(PG_USER):$(PG_PASS)@$(PG_HOST):$(PG_PORT)/$(TEST_DB)?sslmode=disable
export TEST_REDIS_PORT ?= $(REDIS_PORT)

# ── Lint / Vet ──────────────────────────────────────────────
vet:
	$(GO) vet ./...

lint: vet
	@echo "Lint passed (go vet)"

# ── Unit Tests ──────────────────────────────────────────────
test-unit:
	$(GO) test ./chat/... ./config/... ./handlers/... ./payment/... \
		-v -count=1 -timeout=$(TIMEOUT) -race

# ── Integration Tests ───────────────────────────────────────
test-integration:
	$(GO) test ./tests/integration/... \
		-v -count=1 -timeout=$(TIMEOUT)

# ── Security Tests (subset of integration) ──────────────────
test-security:
	$(GO) test ./tests/integration/... \
		-v -count=1 -timeout=$(TIMEOUT) \
		-run "TestSQLInjection|TestCORS|TestBodyLimit|TestBannedUserAccess|TestAuthHeaderVariations"

# ── All Tests ───────────────────────────────────────────────
test: test-unit test-integration

test-all: test

# ── Benchmarks ──────────────────────────────────────────────
test-bench:
	$(GO) test ./chat/... ./handlers/... \
		-bench=Benchmark -benchmem -run="^NOMATCH" -count=1 -timeout=3m

# ── Coverage ────────────────────────────────────────────────
test-coverage:
	$(GO) test ./chat/... ./config/... ./handlers/... ./payment/... \
		-coverprofile=coverage.out -covermode=atomic -timeout=$(TIMEOUT)
	$(GO) tool cover -func=coverage.out
	@echo "HTML report: go tool cover -html=coverage.out -o coverage.html"

# ── Load Tests (k6) ────────────────────────────────────────
load-rest:
	$(K6) run tests/load/rest_load.js --env BASE_URL=$(BASE_URL)

load-ws:
	$(K6) run tests/load/ws_load.js --env WS_URL=$(subst http,ws,$(BASE_URL))

load-spike:
	$(K6) run tests/load/spike_test.js --env BASE_URL=$(BASE_URL)

load-soak:
	$(K6) run tests/load/soak_test.js --env BASE_URL=$(BASE_URL)

# ── Database helpers ────────────────────────────────────────
test-db-create:
	@psql -U $(PG_USER) -h $(PG_HOST) -p $(PG_PORT) -tc \
		"SELECT 1 FROM pg_database WHERE datname='$(TEST_DB)'" | grep -q 1 || \
		psql -U $(PG_USER) -h $(PG_HOST) -p $(PG_PORT) -c "CREATE DATABASE $(TEST_DB)"

test-db-drop:
	psql -U $(PG_USER) -h $(PG_HOST) -p $(PG_PORT) -c "DROP DATABASE IF EXISTS $(TEST_DB)"

# ── Clean ───────────────────────────────────────────────────
clean:
	rm -f coverage.out coverage.html
	$(GO) clean -testcache
