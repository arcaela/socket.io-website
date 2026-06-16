BINARY := mini
BIN_DIR := bin

# Build metadata injected into the `version` command. Override on the command
# line (e.g. `make build VERSION=v1.2.3`) or let git/date fill them in.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build run install fmt vet tidy clean

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) .

run: build
	./$(BIN_DIR)/$(BINARY)

install: build
	install -m 0755 $(BIN_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)
