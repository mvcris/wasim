VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run test test-race cover lint fmt e2e screenshots docker clean

all: fmt lint test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/wasim ./cmd/wasim

run:
	go run ./cmd/wasim

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -count=1 -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run

fmt:
	gofmt -w cmd internal

e2e:
	pnpm --dir e2e install --frozen-lockfile
	pnpm --dir e2e test

screenshots:
	pnpm --dir e2e install --frozen-lockfile
	pnpm --dir e2e screenshots

docker:
	docker build --build-arg VERSION=$(VERSION) -t wasim:$(VERSION) .

clean:
	rm -rf bin dist coverage.out coverage.html e2e/test-results e2e/playwright-report
