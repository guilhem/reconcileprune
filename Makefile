# Makefile for reconcileprune

.PHONY: all
all: test

.PHONY: test
test:
	go test -v -race ./...

.PHONY: test-coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint:
	golangci-lint run -v

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: modules
modules:
	go mod tidy

.PHONY: verify-modules
verify-modules: modules
	@git diff --quiet HEAD -- go.sum go.mod || \
		(echo "go.mod/go.sum are out of date" && exit 1)

.PHONY: clean
clean:
	go clean -testcache
	rm -f coverage.out coverage.html
