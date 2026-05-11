BINARY := mini
BIN_DIR := bin

.PHONY: build run install fmt vet tidy clean

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/$(BINARY) ./cmd/mini

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
