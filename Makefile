.PHONY: build test race vet fmt check

build:
	go build -o bin/lazyrun ./cmd/lazyrun

test:
	go test -count=1 ./...

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
