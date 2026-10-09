.PHONY: build test installer-test race vet fmt check smoke stress fuzz audit release release-check

# Empty uses embedded module/VCS information; releases set VERSION explicitly.
VERSION ?=
GOVULNCHECK_VERSION := v1.1.4

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/lazyrun ./cmd/lazyrun

test:
	go test -count=1 ./...

# Offline installer tests; requires Python 3 and standard Linux packaging tools.
installer-test:
	sh -n scripts/install.sh
	python3 scripts/test-install.py

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race -count=1 ./...

smoke:
	@test -n "$$LAZYRUN_SMOKE_PYTHON" || { echo 'Set LAZYRUN_SMOKE_PYTHON to an absolute venv interpreter (examples/smoke/README.md)' >&2; exit 1; }
	go test -race -count=1 -timeout=3m ./internal/app -run '^TestSmoke' -v

stress:
	go test -race -count=10 -timeout=10m ./internal/runtime ./internal/logstore ./internal/securefs ./internal/supervisor ./internal/transport
	go test -race -count=10 -timeout=10m ./internal/app -run 'Test(ConcurrentBootstrap|SupervisorReconnect|DiskLogs|LogNavigation|ConcurrentBoundedReaders|Dashboard)'

fuzz:
	go test ./internal/gui -run '^$$' -fuzz '^FuzzSanitizer$$' -fuzztime=30s -parallel=4
	go test ./internal/gui -run '^$$' -fuzz '^FuzzWrappedViewport$$' -fuzztime=30s -parallel=4
	go test ./internal/gui -run '^$$' -fuzz '^FuzzSearchAgreesWithTerminalSanitizer$$' -fuzztime=30s -parallel=4
	go test ./internal/logsearch -run '^$$' -fuzz '^FuzzSearchContinuations$$' -fuzztime=30s -parallel=4

# Networked, pinned tooling; not part of the offline-capable default test suite.
audit:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

release-check: check installer-test smoke audit

release:
	VERSION="$(VERSION)" bash scripts/release.sh
