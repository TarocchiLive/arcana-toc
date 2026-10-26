GO           ?= go
BINARY       := arcana-toc
LINT_VERSION := v2.13.2

.PHONY: build test vet lint fmt tidy tools clean

build:
	$(GO) build -o bin/$(BINARY) ./cmd/arcana-toc

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

fmt:
	$(GO) fmt ./...
	gofmt -l .

tidy:
	$(GO) mod tidy

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)

clean:
	rm -rf bin
