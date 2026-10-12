.PHONY: build test installer-test race vet fmt check smoke stress fuzz audit release release-check

# Empty uses embedded module/VCS information; releases set VERSION explicitly.
VERSION ?=
GOVULNCHECK_VERSION := v1.1.4
TEST_TIMEOUT ?= 5m
FUZZ_TIME ?= 30s
FUZZ_PARALLEL ?= 4
# Race-instrumented child processes otherwise sleep one second at every clean
# exit. Tests explicitly wait for their workers; the exit delay adds no coverage.
RACE_ENV = GORACE="$${GORACE:-} atexit_sleep_ms=0"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/lazyrun ./cmd/lazyrun

test:
	go test -count=1 -timeout=$(TEST_TIMEOUT) ./...

# Offline installer tests; requires Python 3 and standard Linux packaging tools.
installer-test:
	sh -n scripts/install.sh
	python3 scripts/test-install.py

race:
	$(RACE_ENV) go test -race -count=1 -timeout=$(TEST_TIMEOUT) ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	$(RACE_ENV) go test -race -count=1 -timeout=$(TEST_TIMEOUT) ./...

smoke:
	@test -n "$$LAZYRUN_SMOKE_PYTHON" || { echo 'Set LAZYRUN_SMOKE_PYTHON to an absolute venv interpreter (examples/smoke/README.md)' >&2; exit 1; }
	$(RACE_ENV) go test -race -count=1 -timeout=3m ./internal/app -run '^TestSmoke' -v

stress:
	$(RACE_ENV) go test -race -count=10 -timeout=10m ./internal/runtime ./internal/logstore ./internal/securefs ./internal/supervisor ./internal/transport
	$(RACE_ENV) go test -race -count=10 -timeout=10m ./internal/app -run 'Test(ConcurrentBootstrap|SupervisorReconnect|DiskLogs|LogNavigation|ConcurrentBoundedReaders|Dashboard)'

fuzz:
	go test ./internal/gui -run '^$$' -fuzz '^FuzzSanitizer$$' -fuzztime=$(FUZZ_TIME) -parallel=$(FUZZ_PARALLEL)
	go test ./internal/gui -run '^$$' -fuzz '^FuzzWrappedViewport$$' -fuzztime=$(FUZZ_TIME) -parallel=$(FUZZ_PARALLEL)
	go test ./internal/gui -run '^$$' -fuzz '^FuzzSearchAgreesWithTerminalSanitizer$$' -fuzztime=$(FUZZ_TIME) -parallel=$(FUZZ_PARALLEL)
	go test ./internal/logsearch -run '^$$' -fuzz '^FuzzSearchContinuations$$' -fuzztime=$(FUZZ_TIME) -parallel=$(FUZZ_PARALLEL)

# Networked, pinned tooling; not part of the offline-capable default test suite.
audit:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

release-check: check installer-test smoke audit

release:
	VERSION="$(VERSION)" bash scripts/release.sh
